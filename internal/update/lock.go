package update

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"goodkind.io/agent-gate/internal/config"
)

// WithLock serializes update work across processes with a filesystem lock.
func WithLock(ctx context.Context, fn func() error) error {
	log := slog.Default()
	if err := ctx.Err(); err != nil {
		log.WarnContext(ctx, "update lock context canceled before acquire", "err", err)
		return fmt.Errorf("context canceled before update lock: %w", err)
	}
	lockPath := filepath.Join(config.DefaultStateDir(), "update.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		log.WarnContext(ctx, "update lock create dir failed", "path", lockPath, "err", err)
		return fmt.Errorf("create update lock dir: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.WarnContext(ctx, "update lock open failed", "path", lockPath, "err", err)
		return fmt.Errorf("open update lock: %w", err)
	}
	defer func() { _ = file.Close() }()
	fd := file.Fd()
	if fd > uintptr(int(^uint32(0)>>1)) {
		return fmt.Errorf("update lock fd %d exceeds int range", fd)
	}
	if err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.WarnContext(ctx, "update lock acquire failed", "path", lockPath, "err", err)
		return fmt.Errorf("update already running: %w", err)
	}
	defer func() { _ = syscall.Flock(int(fd), syscall.LOCK_UN) }()
	if err := ctx.Err(); err != nil {
		log.WarnContext(ctx, "update lock context canceled after acquire", "err", err)
		return fmt.Errorf("context canceled after update lock: %w", err)
	}
	return fn()
}
