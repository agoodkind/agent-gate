package daemon_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func temporalAssignmentServer(
	t *testing.T,
	maxSize string,
) (*daemon.Server, *config.Config, string, string) {
	t.Helper()
	_ = sizeRetentionConfig(t, maxSize, 1)
	tee, err := exec.LookPath("tee")
	if err != nil {
		t.Fatal(err)
	}

	capture := filepath.Join(t.TempDir(), "validator.json")
	body := fmt.Sprintf(
		`
[audit.storage]
max_size = %q
bucket_interval = "1000000h"
retention_buckets = 1

[[rules]]
name = "capture-temporal"
cursor_events = ["stop"]
action = "inject"
output = "fallback"

[[rules.conditions]]
kind = "exec"
command = [%q, %q]
block_on = "zero"
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`,
		maxSize,
		tee,
		capture,
	)
	writeTemporalConfig(t, body)
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	server, err := daemon.New(t.Context(), sizeRetentionLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(server.Close)

	return server, cfg, capture, body
}

func submitTemporalPrompt(t *testing.T, server *daemon.Server, prompt string, padding int) {
	t.Helper()
	request := &daemonpb.EvaluateHookRequest{
		ProviderHint: "cursor",
		RawJson: []byte(
			fmt.Sprintf(
				`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"ongoing","prompt":%q,"padding":%q}`,
				prompt,
				strings.Repeat("x", padding),
			),
		),
	}
	if _, err := server.EvaluateHook(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func evaluateTemporalStop(
	t *testing.T,
	server *daemon.Server,
	capture string,
) (execconcern.Input, string) {
	t.Helper()
	response, err := server.EvaluateHook(
		t.Context(),
		&daemonpb.EvaluateHookRequest{
			ProviderHint: "cursor",
			RawJson: []byte(
				`{"hook_event_name":"stop","conversation_id":"ongoing","status":"completed"}`,
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}

	var input execconcern.Input
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}

	var output struct {
		FollowupMessage string `json:"followup_message"`
	}
	if err := json.Unmarshal(response.StdoutData, &output); err != nil ||
		output.FollowupMessage == "" {
		t.Fatalf("validator did not produce a response: %s %v", response.StdoutData, err)
	}

	return input, output.FollowupMessage
}

func assertCapturedTemporal(
	t *testing.T,
	input execconcern.Input,
	field string,
	expected string,
	available bool,
) {
	t.Helper()

	for _, value := range input.Matched {
		if value.Field == field {
			if value.Available == nil || *value.Available != available || value.Value != expected {
				t.Fatalf("%s = %+v, want value %q available %v", field, value, expected, available)
			}

			return
		}
	}

	t.Fatalf("validator input omitted %s", field)
}

func TestTemporalPromptAndResponseAcrossActualSizeRotation(t *testing.T) {
	server, cfg, capture, _ := temporalAssignmentServer(t, "16MiB")

	for range 6 {
		submitTemporalPrompt(t, server, "before rotation", 0)
	}

	_, previousResponse := evaluateTemporalStop(t, server, capture)
	submitTemporalPrompt(t, server, "last prompt in old file", 1024*1024)
	old := sizeRetentionStatus(t, cfg)
	server.StartAuditScheduler(t.Context())
	waitSizeRetention(
		t,
		func() bool { return sizeRetentionStatus(t, cfg).CurrentBucketID != old.CurrentBucketID },
	)
	current := sizeRetentionStatus(t, cfg)
	if !current.NextBoundary.Equal(old.NextBoundary) {
		t.Fatal("size rotation changed the time window")
	}

	waitSizeRetention(
		t,
		func() bool {
			submitTemporalPrompt(t, server, "first prompt in new file", 0)
			records, err := evaluation.Query(
				t.Context(),
				cfg,
				evaluation.QueryFilter{
					BucketID:  current.CurrentBucketID,
					ReceiptID: 1,
					Mode:      "hot",
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			return len(records.Records) == 1 && records.Records[0].EventName == "beforeSubmitPrompt"
		},
	)
	input, currentResponse := evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "first prompt in new file", true)
	assertCapturedTemporal(t, input, "last_response_output", previousResponse, true)
	input, _ = evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_response_output", currentResponse, true)
}

func TestTemporalAssignmentReloadAndResetReusedPath(t *testing.T) {
	server, cfg, capture, body := temporalAssignmentServer(t, "0B")

	for range 6 {
		submitTemporalPrompt(t, server, "original prompt", 0)
	}

	_, previousResponse := evaluateTemporalStop(t, server, capture)
	old := sizeRetentionStatus(t, cfg)
	cfg = reloadTemporalAssignment(t, server, body+"\n# Reload the same file assignment.\n")
	if sizeRetentionStatus(t, cfg).CurrentBucketPath != old.CurrentBucketPath {
		t.Fatal("configuration reload changed the file assignment")
	}

	input, _ := evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "original prompt", true)
	assertCapturedTemporal(t, input, "last_response_output", previousResponse, true)
	// Both intervals start at the Unix epoch during this test's supported dates.
	cfg = reloadTemporalAssignment(
		t,
		server,
		strings.Replace(body, `bucket_interval = "1000000h"`, `bucket_interval = "2000000h"`, 1),
	)
	if sizeRetentionStatus(t, cfg).CurrentBucketPath != old.CurrentBucketPath {
		t.Fatal("reset test did not reuse the original physical path")
	}

	submitTemporalPrompt(t, server, "prompt after reset", 0)
	input, currentResponse := evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "prompt after reset", true)
	input, _ = evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_response_output", currentResponse, true)
}

func TestTemporalOversizedPromptReportsUnavailableToValidator(t *testing.T) {
	server, _, capture, body := temporalAssignmentServer(t, "0B")
	body = "[performance.hook.cache]\nmax_value_bytes = 64\nmax_bytes = 512\n" + body
	_ = reloadTemporalAssignment(t, server, body)
	submitTemporalPrompt(t, server, "small prompt", 0)
	input, _ := evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "small prompt", true)
	submitTemporalPrompt(t, server, strings.Repeat("x", 65), 0)
	input, _ = evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "", false)
	assertCapturedTemporal(t, input, "last_response_output", "", false)
}

func writeTemporalConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func reloadTemporalAssignment(t *testing.T, server *daemon.Server, body string) *config.Config {
	t.Helper()
	writeTemporalConfig(t, body)
	expected, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	hash, err := expected.Identity()
	if err != nil {
		t.Fatal(err)
	}

	waitSizeRetention(
		t,
		func() bool {
			if _, err := server.EvaluateHook(t.Context(), sizeRetentionRequest(0)); err != nil {
				t.Fatal(err)
			}

			result, err := evaluation.Query(
				t.Context(),
				expected,
				evaluation.QueryFilter{Mode: "hot", Limit: 1},
			)

			return err == nil && len(result.Records) == 1 && result.Records[0].ConfigHash == hash
		},
	)

	return expected
}

func TestTemporalHistoricalReplayDoesNotPromoteOldContext(t *testing.T) {
	server, cfg, capture, _ := temporalAssignmentServer(t, "16MiB")
	submitTemporalPrompt(t, server, "historical prompt", 1024*1024)
	_, _ = evaluateTemporalStop(t, server, capture)
	submitTemporalPrompt(t, server, "latest hot prompt", 0)
	_, latestResponse := evaluateTemporalStop(t, server, capture)
	old := sizeRetentionStatus(t, cfg)
	readers, err := cfg.ReadAuditHistory(t.Context(), old.CurrentBucketID)
	if err != nil {
		t.Fatal(err)
	}

	if len(readers.Handles) != 1 {
		t.Fatalf("history handles = %d", len(readers.Handles))
	}

	bucket := readers.Handles[0].Bucket
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}

	handle, err := sizeRetentionCatalog(t, cfg).OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = handle.Close() }()
	store, err := intake.NewStore(
		t.Context(),
		handle.Database,
		cfg.AuditStoragePolicy(),
		sizeRetentionLogger(),
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, receipt := range []int64{3, 4} {
		if err := store.MarkDeferredComplete(t.Context(), receipt); err != nil {
			t.Fatal(err)
		}
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	server.StartAuditScheduler(t.Context())
	waitSizeRetention(
		t,
		func() bool {
			if sizeRetentionStatus(t, cfg).CurrentBucketID == old.CurrentBucketID {
				return false
			}

			result, err := evaluation.Query(
				t.Context(),
				cfg,
				evaluation.QueryFilter{
					BucketID: old.CurrentBucketID,
					Mode:     "deferred",
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			return len(result.Records) == 2
		},
	)
	input, _ := evaluateTemporalStop(t, server, capture)
	assertCapturedTemporal(t, input, "last_user_message", "latest hot prompt", true)
	assertCapturedTemporal(t, input, "last_response_output", latestResponse, true)
}
