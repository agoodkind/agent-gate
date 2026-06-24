package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
)

// Run starts the daemon gRPC server on the XDG runtime Unix socket.
// It blocks until the server stops. The cfg argument may be nil. In that
// case the daemon falls back to default XDG paths.
func Run(log *slog.Logger, cfg *config.Config) error {
	ctx := context.Background()
	if err := config.EnsureRuntimeDir(); err != nil {
		log.ErrorContext(ctx, "ensure runtime dir failed", "err", err)
		return fmt.Errorf("ensure runtime dir: %w", err)
	}

	lockPath := filepath.Join(config.RuntimeDir(), "daemon.process.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		log.ErrorContext(ctx, "open daemon process lock failed", "path", lockPath, "err", err)
		return fmt.Errorf("open daemon process lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	lockFD := lockFile.Fd()
	if lockFD > uintptr(int(^uint32(0)>>1)) {
		log.ErrorContext(ctx, "daemon process lock fd out of range", "fd", lockFD)
		return fmt.Errorf("daemon process lock fd %d exceeds int range", lockFD)
	}
	lockFDInt := int(lockFD)
	if err := syscall.Flock(lockFDInt, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.InfoContext(ctx, "daemon already running", "lock_path", lockPath)
		return nil
	}
	defer func() { _ = syscall.Flock(lockFDInt, syscall.LOCK_UN) }()

	socketPath := config.DaemonSocketPath()
	listener, err := daemonListener(ctx, socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	srv, err := New(log, cfg)
	if err != nil {
		log.ErrorContext(ctx, "create daemon server failed", "err", err)
		return fmt.Errorf("create daemon server: %w", err)
	}
	defer func() { srv.Close() }()

	grpcServer := grpc.NewServer()
	daemonpb.RegisterAgentGateDServer(grpcServer, srv)
	srv.StartUpdateScheduler(ctx, func() {
		grpcServer.GracefulStop()
	})

	log.InfoContext(ctx, "daemon listening", "socket", socketPath)
	if err := grpcServer.Serve(listener); err != nil {
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		log.ErrorContext(ctx, "grpc serve failed", "err", err)
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

func daemonListener(ctx context.Context, socketPath string) (net.Listener, error) {
	log := slog.Default()
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		log.ErrorContext(ctx, "remove stale daemon socket failed", "socket", socketPath, "err", err)
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		log.ErrorContext(ctx, "listen on daemon socket failed", "socket", socketPath, "err", err)
		return nil, fmt.Errorf("listen on %s: %w", socketPath, err)
	}
	return listener, nil
}
