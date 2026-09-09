package setup

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
)

func TestInstalledProbeFollowsRecordedBucketAcrossCommandRollover(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	cfg := &config.Config{}
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().UTC().Truncate(24 * time.Hour)
	before := boundary.Add(-time.Nanosecond)
	old, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), before)
	if err != nil {
		t.Fatal(err)
	}
	hooks := installer.DefaultHooksOptions("/usr/bin/true")
	hooks.HomeDir, hooks.Providers = root, []installer.Provider{installer.ProviderCodex}
	plan, err := installer.PrepareHookInstallation(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.ApplyHookInstallation(plan); err != nil {
		t.Fatal(err)
	}
	write := func(bucket auditstorage.Bucket, session string) {
		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
		store, err := intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := store.Append(t.Context(), intake.Record{EventID: "same-event", System: "codex", SessionID: session, EventName: "SessionStart", RecordedAt: before, RawPayload: []byte(`{}`)})
		if err != nil || receipt.ReceiptID != 1 {
			t.Fatalf("receipt = %+v, %v", receipt, err)
		}
		if err := store.Evaluations().RecordCompleted(t.Context(), evaluation.Record{Evaluation: evaluation.Evaluation{EvaluationID: "same-evaluation", EventID: receipt.EventID, ReceiptID: receipt.ReceiptID, Mode: "hot", Attempt: 1, StartedAt: before, CompletedAt: before, FinalVerdict: "allow"}}); err != nil {
			t.Fatal(err)
		}
		if err := audit.WriteEvents(t.Context(), handle.Database, []audit.Event{{EventID: "same-audit", Time: before.Format(time.RFC3339Nano), System: "codex", SessionID: session, EventName: "SessionStart", Decision: audit.Decision{Kind: "allow"}}}); err != nil {
			t.Fatal(err)
		}
	}
	execute := func(ctx context.Context, command installer.ManagedHookCommand, payload []byte) (int, []byte, error) {
		code, output, err := executeLifecycleProbe(ctx, command, payload)
		if err != nil {
			return code, output, err
		}
		write(old, "setup-rollover")
		current, err := catalog.EnsureCurrent(ctx, cfg.AuditStoragePolicy().Rotation(), boundary)
		if err != nil {
			return code, output, err
		}
		write(current, "other-session")
		return code, output, nil
	}
	result, err := verifyInstalledHookWithExecutor(t.Context(), ProbeRequest{SetupID: "setup-rollover", HomeDir: root, BinPath: "/usr/bin/true", Config: cfg}, installer.ProviderCodex, time.Second, execute)
	if err != nil {
		t.Fatal(err)
	}
	if result.BucketID != old.ID || result.ReceiptID != 1 || result.EvaluationID != "same-evaluation" || result.AuditEventID != "same-audit" {
		t.Fatalf("rollover proof = %+v", result)
	}
}
