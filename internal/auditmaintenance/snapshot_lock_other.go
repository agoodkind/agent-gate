//go:build !unix

package auditmaintenance

import (
	"context"
	"errors"
)

func holdSnapshotCheckpointLock(context.Context, string) (func() error, error) {
	return nil, errors.New("read-only audit snapshots are unsupported on this platform")
}
