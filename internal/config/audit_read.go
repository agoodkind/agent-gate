package config

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

var auditReadNow = time.Now

// ReadAuditHistory pins the retained catalog without creating database history.
func (c *Config) ReadAuditHistory(ctx context.Context, bucketID string) (*auditstorage.ReadSet, error) {
	var cfg Config
	if c != nil {
		cfg = *c
	}
	if err := cfg.PrepareAuditStorage(); err != nil {
		slog.WarnContext(ctx, "read audit catalog failed", "err", err)
		return nil, fmt.Errorf("read audit catalog: %w", err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		slog.WarnContext(ctx, "read audit catalog failed", "err", err)
		return nil, fmt.Errorf("read audit catalog: %w", err)
	}
	set, err := catalog.Read(ctx, cfg.AuditStoragePolicy().Rotation(), auditReadNow())
	if err != nil {
		slog.WarnContext(ctx, "read audit catalog failed", "err", err)
		return nil, fmt.Errorf("read audit catalog: %w", err)
	}
	if bucketID == "" {
		return set, nil
	}
	for _, handle := range set.Handles {
		if handle.Bucket.ID == bucketID {
			for _, other := range set.Handles {
				if other != handle {
					_ = other.Close()
				}
			}
			set.Handles = []*auditstorage.BucketHandle{handle}
			return set, nil
		}
	}
	_ = set.Close()
	return nil, fmt.Errorf("bucket %q is outside retained history", bucketID)
}
