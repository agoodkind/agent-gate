package intake_test

import (
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func openQueryBucket(t *testing.T, path string, policy *config.AuditStoragePolicy) *intake.Store {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(filepath.Dir(path), "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(filepath.Dir(path), "runtime"))
	cfg := queryConfig(path)
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	selected := cfg.AuditStoragePolicy()
	if policy != nil {
		selected = *policy
	}
	store, err := intake.NewStore(t.Context(), handle.Database, selected, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
