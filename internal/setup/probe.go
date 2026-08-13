// Package setup verifies installed agent-gate integrations through public boundaries.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
)

const (
	defaultProbeTimeout = 10 * time.Second
	probePollInterval   = 25 * time.Millisecond
)

// ProbeRequest selects installed providers and the durable store used for verification.
type ProbeRequest struct {
	SetupID   string
	Providers []installer.Provider
	HomeDir   string
	BinPath   string
	Config    *config.Config
	Timeout   time.Duration
}

// ProbeResult identifies every durable stage produced by one installed command.
type ProbeResult struct {
	Provider      installer.Provider
	IntakeEventID string
	ReceiptID     int64
	EvaluationID  string
	AuditEventID  string
	Decision      string
	ExitCode      int
}

// VerifyInstalledHooks executes installed lifecycle commands and verifies durable allow results.
func VerifyInstalledHooks(ctx context.Context, request ProbeRequest) ([]ProbeResult, error) {
	if request.SetupID == "" {
		return nil, errors.New("setup ID is required")
	}
	if request.Config == nil {
		return nil, errors.New("setup configuration is required")
	}
	if request.BinPath == "" {
		return nil, errors.New("expected binary path is required")
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	results := make([]ProbeResult, 0, len(request.Providers))
	for _, provider := range request.Providers {
		result, err := verifyInstalledHook(ctx, request, provider, timeout)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func verifyInstalledHook(
	ctx context.Context,
	request ProbeRequest,
	provider installer.Provider,
	timeout time.Duration,
) (ProbeResult, error) {
	command, err := installer.ReadManagedLifecycleCommandContext(
		ctx,
		installer.HooksOptions{
			BinPath: request.BinPath, TemplatesDir: "", HomeDir: request.HomeDir,
			Stdout: nil, Providers: nil,
		},
		provider,
	)
	if err != nil {
		slog.WarnContext(ctx, "read installed lifecycle command failed", "provider", provider, "err", err)
		return ProbeResult{}, fmt.Errorf("%s: read installed lifecycle command: %w", provider, err)
	}
	payload, err := marshalLifecycleProbePayload(provider, request.SetupID)
	if err != nil {
		return ProbeResult{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	exitCode, output, err := executeLifecycleProbe(probeCtx, command, payload)
	if err != nil {
		return ProbeResult{}, fmt.Errorf(
			"%s: installed lifecycle command exited %d: %w: %s",
			provider,
			exitCode,
			err,
			bytes.TrimSpace(output),
		)
	}
	result := ProbeResult{
		Provider: provider, IntakeEventID: "", ReceiptID: 0, EvaluationID: "",
		AuditEventID: "", Decision: "", ExitCode: exitCode,
	}
	if err := waitForDurableProbe(probeCtx, request, command, &result); err != nil {
		return ProbeResult{}, err
	}
	return result, nil
}

func executeLifecycleProbe(
	ctx context.Context,
	command installer.ManagedHookCommand,
	payload []byte,
) (int, []byte, error) {
	slog.DebugContext(ctx, "run installed lifecycle command", "provider", command.Provider)
	// The reader validates the executable and exact managed arguments before this call.
	// #nosec G204 -- The executable and arguments come from validated installed configuration.
	process := exec.CommandContext(ctx, command.Executable, command.Arguments...)
	process.Stdin = bytes.NewReader(payload)
	output, err := process.CombinedOutput()
	if err == nil {
		slog.DebugContext(ctx, "installed lifecycle command completed", "provider", command.Provider, "exit_code", 0)
		return 0, output, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		slog.WarnContext(ctx, "installed lifecycle command failed", "provider", command.Provider, "exit_code", exitError.ExitCode(), "err", err)
		return exitError.ExitCode(), output, fmt.Errorf("run installed lifecycle command: %w", err)
	}
	slog.WarnContext(ctx, "start installed lifecycle command failed", "provider", command.Provider, "err", err)
	return -1, output, fmt.Errorf("start installed lifecycle command: %w", err)
}

func marshalLifecycleProbePayload(provider installer.Provider, setupID string) ([]byte, error) {
	fields := map[string]string{}
	switch provider {
	case installer.ProviderClaude, installer.ProviderCodex, installer.ProviderGemini:
		fields["hook_event_name"] = "SessionStart"
		fields["session_id"] = setupID
	case installer.ProviderCursor:
		fields["hook_event_name"] = "sessionStart"
		fields["session_id"] = setupID
	case installer.ProviderCopilot:
		fields["sessionId"] = setupID
	default:
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		slog.Warn("encode lifecycle probe failed", "provider", provider, "err", err)
		return nil, fmt.Errorf("%s: encode lifecycle probe: %w", provider, err)
	}
	return payload, nil
}

func waitForDurableProbe(
	ctx context.Context,
	request ProbeRequest,
	command installer.ManagedHookCommand,
	result *ProbeResult,
) error {
	for {
		complete, err := readDurableProbe(ctx, request, command, result)
		if err != nil {
			return err
		}
		if complete {
			return nil
		}
		select {
		case <-ctx.Done():
			return probeTimeoutError(command.Provider, result, request.Timeout)
		case <-time.After(probePollInterval):
		}
	}
}

func readDurableProbe(
	ctx context.Context,
	request ProbeRequest,
	command installer.ManagedHookCommand,
	result *ProbeResult,
) (bool, error) {
	if result.IntakeEventID == "" {
		return readDurableIntake(ctx, request, command, result)
	}
	if result.EvaluationID == "" {
		return readDurableEvaluation(ctx, request, command, result)
	}
	if result.AuditEventID == "" {
		return readDurableAudit(ctx, request, command, result)
	}
	return true, nil
}

func readDurableIntake(
	ctx context.Context,
	request ProbeRequest,
	command installer.ManagedHookCommand,
	result *ProbeResult,
) (bool, error) {
	provider := string(command.Provider)
	query, err := intake.Query(ctx, request.Config, intake.QueryFilter{
		Since: time.Time{}, Until: time.Time{}, System: provider,
		SessionID: request.SetupID, EventName: command.EventName, ToolName: "",
		DeferredState: "", EventID: "", Limit: 2,
		IncludeNormalized: false, IncludeEnv: false,
	})
	if err != nil {
		slog.WarnContext(ctx, "query durable intake failed", "provider", provider, "err", err)
		return false, fmt.Errorf("%s: query durable intake: %w", provider, err)
	}
	if len(query.Records) == 0 {
		return false, nil
	}
	if len(query.Records) != 1 {
		return false, fmt.Errorf("%s: durable intake returned %d records", provider, len(query.Records))
	}
	result.IntakeEventID = query.Records[0].EventID
	return false, nil
}

func readDurableEvaluation(
	ctx context.Context,
	request ProbeRequest,
	command installer.ManagedHookCommand,
	result *ProbeResult,
) (bool, error) {
	provider := string(command.Provider)
	for _, mode := range []string{"hot", "deferred", "deferred_replay"} {
		query, err := evaluation.Query(ctx, request.Config.AuditSQLitePath(), evaluation.QueryFilter{
			EvaluationID: "", EventID: result.IntakeEventID, ReceiptID: 0, Mode: mode,
			Since: time.Time{}, Until: time.Time{}, System: provider,
			SessionID: request.SetupID, EventName: command.EventName, ToolName: "",
			RuleName: "", LayerName: "", LayerKind: "", LayerOutcome: "", ModelName: "",
			FinalVerdict: "", DetailMode: evaluation.QueryDetailSummary,
			CompleteDetailOnly: false, Limit: 2, Offset: 0,
		})
		if err != nil {
			slog.WarnContext(ctx, "query durable evaluation failed", "provider", provider, "mode", mode, "err", err)
			return false, fmt.Errorf("%s: query durable evaluation: %w", provider, err)
		}
		if len(query.Records) == 0 {
			continue
		}
		if len(query.Records) != 1 {
			return false, fmt.Errorf("%s: durable evaluation returned %d %s records", provider, len(query.Records), mode)
		}
		record := query.Records[0]
		if record.ReceiptID <= 0 || record.EvaluationID == "" || record.CompletedAt.IsZero() {
			return false, nil
		}
		if record.FinalVerdict != "allow" {
			return false, fmt.Errorf("%s: durable evaluation decision is %q, want allow", provider, record.FinalVerdict)
		}
		result.ReceiptID = record.ReceiptID
		result.EvaluationID = record.EvaluationID
		return false, nil
	}
	return false, nil
}

func readDurableAudit(
	ctx context.Context,
	request ProbeRequest,
	command installer.ManagedHookCommand,
	result *ProbeResult,
) (bool, error) {
	provider := string(command.Provider)
	records, _, err := audit.QueryReadOnly(ctx, request.Config, audit.QueryFilter{
		Since: time.Time{}, Until: time.Time{}, System: provider,
		SessionID: request.SetupID, EventName: command.EventName, ToolName: "",
		Decision: "", Rule: "", Limit: 2,
	})
	if err != nil {
		slog.WarnContext(ctx, "query durable audit failed", "provider", provider, "err", err)
		return false, fmt.Errorf("%s: query durable audit: %w", provider, err)
	}
	if len(records) == 0 {
		return false, nil
	}
	if len(records) != 1 {
		return false, fmt.Errorf("%s: durable audit returned %d records", provider, len(records))
	}
	if records[0].Decision.Kind != "allow" {
		return false, fmt.Errorf("%s: durable audit decision is %q, want allow", provider, records[0].Decision.Kind)
	}
	result.AuditEventID = records[0].EventID
	result.Decision = records[0].Decision.Kind
	return true, nil
}

func probeTimeoutError(
	provider installer.Provider,
	result *ProbeResult,
	timeout time.Duration,
) error {
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	stage := "intake"
	if result.IntakeEventID != "" {
		stage = "evaluation"
	}
	if result.EvaluationID != "" {
		stage = "audit"
	}
	return fmt.Errorf("%s: durable %s was not recorded before %s", provider, stage, timeout)
}
