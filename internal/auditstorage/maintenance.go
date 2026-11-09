package auditstorage

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"time"
)

// FileBytes measures the database and its sidecars without opening SQLite.
func FileBytes(path string) (int64, error) { return bucketBytes(path) }

// MaintenanceSize reconciles the actual family size after retention work.
func (catalog *Catalog) MaintenanceSize(
	ctx context.Context,
	policy RotationPolicy,
	now time.Time,
) (int64, bool, error) {
	var total int64
	remaining := false
	err := visitFamily(
		ctx,
		catalog.options.BasePath,
		func(path string) error {
			size, err := bucketBytes(path)
			if err != nil {
				return err
			}

			if size > math.MaxInt64-total {
				return errors.New("audit storage size overflows bytes")
			}

			total += size
			bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(path))
			if size != 0 && (!recognized || !retained(bucket, policy, now)) {
				remaining = true
			}

			return nil
		},
	)
	if err != nil {
		return 0, false, err
	}

	state, err := catalog.loadStateSummary(ctx)

	return total, remaining || len(state.DeferredFiles) != 0, err
}
