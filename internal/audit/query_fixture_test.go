package audit_test

import (
	"database/sql"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func retainedQueryDatabase(t *testing.T, cfg *config.Config) *sql.DB {
	t.Helper()
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
	return handle.Database
}
