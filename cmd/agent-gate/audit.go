package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

var auditExecutablePath = os.Executable
var auditInspectService = installer.InspectService
var auditCommandContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

const auditServiceStatusTimeout = 30 * time.Second

type auditCommandName string

const (
	auditCommandCompact  auditCommandName = "compact"
	auditCommandMaintain auditCommandName = "maintain"
	auditCommandStatus   auditCommandName = "status"
)

func runAudit(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-gate audit status | maintain | compact")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit: config load failed: %v\n", err)
		return 1
	}
	switch auditCommandName(args[0]) {
	case auditCommandCompact:
		return runAuditCompact(args[1:], stdout, stderr, cfg)
	case auditCommandStatus:
		return runAuditStatus(args[1:], stdout, stderr, cfg)
	case auditCommandMaintain:
		return runAuditMaintain(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "agent-gate audit: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runAuditCompact(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	cfg *config.Config,
) int {
	flags := flag.NewFlagSet("audit compact", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dryRun bool
	var apply bool
	var full bool
	flags.BoolVar(&dryRun, "dry-run", false, "preview compaction without writing")
	flags.BoolVar(&apply, "apply", false, "apply incremental compaction")
	flags.BoolVar(&full, "full", false, "preflight full offline compaction")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || dryRun == apply {
		fmt.Fprintln(stderr, "usage: agent-gate audit compact [--full] --dry-run | --apply")
		return 2
	}
	if full && apply {
		fmt.Fprintln(stderr, "agent-gate audit compact: full compaction apply is not implemented")
		return 1
	}
	policy := cfg.AuditStoragePolicy()
	if dryRun {
		if full {
			return runAuditFullCompactDryRun(stdout, stderr, cfg)
		}
		plan, err := auditmaintenance.PreviewCompact(
			context.Background(),
			cfg.AuditSQLitePath(),
			policy.MaintenanceBatchRows,
		)
		if err != nil {
			fmt.Fprintf(stderr, "agent-gate audit compact: %v\n", err)
			return 1
		}
		writeAuditCompactPlan(stdout, plan)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := auditmaintenance.Compact(ctx, auditmaintenance.ApplyOptions{
		Path: cfg.AuditSQLitePath(), Policy: policy, Now: time.Now().UTC(),
		Owner: "agent-gate-cli", LeaseTTL: 5 * time.Minute, Log: nil,
	})
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit compact: %v\n", err)
		return 1
	}
	writeAuditCompactResult(stdout, result)
	return 0
}

func runAuditFullCompactDryRun(stdout io.Writer, stderr io.Writer, cfg *config.Config) int {
	ctx, stop := auditCommandContext()
	defer stop()
	executablePath, err := auditExecutablePath()
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit compact: locate executable: %v\n", err)
		return 1
	}
	canonicalExecutable, err := installer.CanonicalExecutablePath(executablePath)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit compact: resolve executable: %v\n", err)
		return 1
	}
	serviceCtx, cancelService := context.WithTimeout(ctx, auditServiceStatusTimeout)
	service, err := auditInspectService(serviceCtx, installer.ServiceStatusOptions{
		BinaryPath: canonicalExecutable,
	})
	cancelService()
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit compact: service status: %v\n", err)
		return 1
	}
	plan, err := auditmaintenance.PreviewFullCompact(
		ctx,
		auditmaintenance.FullCompactOptions{Path: cfg.AuditSQLitePath(), Service: service},
	)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit compact: full preflight: %v\n", err)
		return 1
	}
	writeAuditFullCompactPlan(stdout, plan)
	return 0
}

func writeAuditFullCompactPlan(writer io.Writer, plan auditmaintenance.FullCompactPlan) {
	fmt.Fprintf(writer, "database path: %s\n", plan.DatabasePath)
	fmt.Fprintf(writer, "database bytes: %d\n", plan.DatabaseSize.DatabaseBytes)
	fmt.Fprintf(writer, "write-ahead log bytes: %d\n", plan.DatabaseSize.WALBytes)
	fmt.Fprintf(writer, "free bytes: %d\n", plan.FreeBytes)
	fmt.Fprintf(writer, "required free bytes: %d\n", plan.RequiredFreeBytes)
	fmt.Fprintf(writer, "service platform: %s\n", plan.Service.Platform)
	fmt.Fprintf(writer, "managed: %t\n", plan.Service.Managed)
	fmt.Fprintf(writer, "running: %t\n", plan.Service.Running)
	fmt.Fprintf(writer, "service binary: %s\n", plan.Service.BinaryPath)
	fmt.Fprintln(writer, "integrity: ok")
	fmt.Fprintf(writer, "hook impact: %s; stop the managed daemon before full apply\n", plan.HookImpact)
}

func runAuditStatus(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	cfg *config.Config,
) int {
	flags := flag.NewFlagSet("audit status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var jsonOutput bool
	var check bool
	flags.BoolVar(&jsonOutput, "json", false, "print status as JSON")
	flags.BoolVar(&check, "check", false, "fail for actionable storage conditions")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "agent-gate audit status: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	status, err := auditmaintenance.ReadStatus(
		context.Background(), cfg.AuditSQLitePath(), cfg.AuditStoragePolicy(), time.Now().UTC(),
	)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit status: %v\n", err)
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(status); err != nil {
			fmt.Fprintf(stderr, "agent-gate audit status: encode: %v\n", err)
			return 1
		}
	} else {
		writeAuditStatus(stdout, status)
	}
	if check && auditStatusNeedsAction(status) {
		return 1
	}
	return 0
}

func runAuditMaintain(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	cfg *config.Config,
) int {
	flags := flag.NewFlagSet("audit maintain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dryRun bool
	var apply bool
	flags.BoolVar(&dryRun, "dry-run", false, "preview maintenance without writing")
	flags.BoolVar(&apply, "apply", false, "apply maintenance")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || dryRun == apply {
		fmt.Fprintln(stderr, "usage: agent-gate audit maintain --dry-run | --apply")
		return 2
	}
	if apply {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result, err := auditmaintenance.Apply(ctx, auditmaintenance.ApplyOptions{
			Path: cfg.AuditSQLitePath(), Policy: cfg.AuditStoragePolicy(),
			Now: time.Now().UTC(), Owner: "agent-gate-cli", LeaseTTL: 5 * time.Minute,
		})
		if err != nil {
			fmt.Fprintf(stderr, "agent-gate audit maintain: %v\n", err)
			return 1
		}
		writeAuditResult(stdout, result)
		return 0
	}
	plan, err := auditmaintenance.Preview(
		context.Background(), cfg.AuditSQLitePath(), cfg.AuditStoragePolicy(), time.Now().UTC(),
	)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit maintain: %v\n", err)
		return 1
	}
	writeAuditPlan(stdout, plan)
	return 0
}

func writeAuditResult(writer io.Writer, result auditmaintenance.Result) {
	if result.Result == "deferred" {
		_, _ = fmt.Fprintln(writer, "maintenance deferred: another maintenance run or database writer is active")
	}
	if result.RunID != "" {
		_, _ = fmt.Fprintf(writer, "run id: %s\n", result.RunID)
	}
	_, _ = fmt.Fprintf(writer, "result: %s\n", result.Result)
	_, _ = fmt.Fprintf(writer, "detail graphs: %d\n", result.DetailGraphs)
	_, _ = fmt.Fprintf(writer, "summary graphs: %d\n", result.SummaryGraphs)
	_, _ = fmt.Fprintf(writer, "size state: %s\n", result.SizeState)
	_, _ = fmt.Fprintf(writer, "reclaimed bytes: %d\n", result.ReclaimedBytes)
	if result.ErrorClass != "" {
		_, _ = fmt.Fprintf(writer, "error class: %s\n", result.ErrorClass)
	}
	if result.NextDueAt != nil {
		_, _ = fmt.Fprintf(writer, "next due at: %s\n", result.NextDueAt.Format(time.RFC3339Nano))
	}
}

func writeAuditCompactPlan(writer io.Writer, plan auditmaintenance.CompactPlan) {
	_, _ = fmt.Fprintf(writer, "auto-vacuum mode: %d\n", plan.AutoVacuumMode)
	_, _ = fmt.Fprintf(writer, "free pages: %d\n", plan.FreePages)
	_, _ = fmt.Fprintf(writer, "pages to reclaim: %d\n", plan.PagesToReclaim)
	if plan.FullModeRequired {
		_, _ = fmt.Fprintln(writer, "full compaction needed: yes")
	} else {
		_, _ = fmt.Fprintln(writer, "full compaction needed: no")
	}
}

func writeAuditCompactResult(writer io.Writer, result auditmaintenance.Result) {
	writeAuditCompactPlan(writer, result.CompactPlan)
	writeAuditResult(writer, result)
}

func writeAuditStatus(writer io.Writer, status auditmaintenance.Status) {
	_, _ = fmt.Fprintf(writer, "database bytes: %d\n", status.DatabaseBytes)
	_, _ = fmt.Fprintf(writer, "write-ahead log bytes: %d\n", status.WALBytes)
	_, _ = fmt.Fprintf(writer, "protected graphs: %d\n", status.ProtectedGraphs)
	_, _ = fmt.Fprintf(writer, "protected bytes estimate: %d\n", status.ProtectedBytes)
	_, _ = fmt.Fprintf(writer, "reclaimable pages: %d\n", status.ReclaimablePages)
	_, _ = fmt.Fprintf(writer, "size state: %s\n", status.SizeState)
	if status.IntegrityOK {
		_, _ = fmt.Fprintln(writer, "integrity: ok")
	} else {
		_, _ = fmt.Fprintf(writer, "integrity: failed: %s\n", status.IntegrityError)
	}
	if status.Overdue {
		_, _ = fmt.Fprintln(writer, "maintenance overdue: yes")
	} else {
		_, _ = fmt.Fprintln(writer, "maintenance overdue: no")
	}
}

func writeAuditPlan(writer io.Writer, plan auditmaintenance.Plan) {
	_, _ = fmt.Fprintf(writer, "planned at: %s\n", plan.PlannedAt.Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(writer, "policy hash: %s\n", plan.PolicyHash)
	_, _ = fmt.Fprintf(writer, "detail candidate graphs: %d\n", plan.DetailCandidateGraphs)
	_, _ = fmt.Fprintf(writer, "summary candidate graphs: %d\n", plan.SummaryCandidateGraphs)
	_, _ = fmt.Fprintf(writer, "protected graphs: %d\n", plan.ProtectedGraphs)
	_, _ = fmt.Fprintf(writer, "protected bytes estimate: %d\n", plan.ProtectedBytes)
	_, _ = fmt.Fprintf(writer, "estimated delete bytes: %d\n", plan.EstimatedDeleteBytes)
}

func auditStatusNeedsAction(status auditmaintenance.Status) bool {
	if status.Overdue || !status.IntegrityOK {
		return true
	}
	return status.SizeState == auditmaintenance.SizeStateOverTarget ||
		status.SizeState == auditmaintenance.SizeStateReclaimPending
}
