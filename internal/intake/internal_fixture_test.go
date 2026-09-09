package intake

import (
	"context"
	"log/slog"
	"testing"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func openFixtureIntake(t testing.TB, ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	t.Helper()
	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		return nil, err
	}
	database, err := auditstorage.OpenWriter(ctx, path)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = database.Close() })
	return NewStore(ctx, database, cfg.AuditStoragePolicy(), log)
}
