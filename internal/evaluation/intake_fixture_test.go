package evaluation_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func openFixtureIntake(t testing.TB, ctx context.Context, path string, log *slog.Logger) (*intake.Store, error) {
	t.Helper()
	cfg := fixtureConfig(t, path)
	if err := cfg.PrepareAuditStorage(); err != nil {
		return nil, err
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		return nil, err
	}
	bucket, err := catalog.EnsureCurrent(ctx, cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		return nil, err
	}
	handle, err := catalog.OpenWriter(ctx, bucket)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = handle.Close() })
	return intake.NewStore(ctx, handle.Database, cfg.AuditStoragePolicy(), log)
}

func fixtureConfig(t testing.TB, path string) *config.Config {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(filepath.Dir(path), "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(filepath.Dir(path), "runtime"))
	cfg := &config.Config{}
	cfg.Audit.Outputs.SQLite.Path = path
	return cfg
}
