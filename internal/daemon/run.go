package daemon

import (
	"context"
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
	if err := config.EnsureRuntimeDir(); err != nil {
		return err
	}

	lockPath := filepath.Join(config.RuntimeDir(), "daemon.process.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon process lock: %w", err)
	}
	defer func() { _ = lockFile.Close() }()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		log.InfoContext(context.Background(), "daemon already running", "lock_path", lockPath)
		return nil
	}
	defer func() { _ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN) }()

	socketPath := config.DaemonSocketPath()
	listener, err := daemonListener(socketPath)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	srv, err := New(log, cfg)
	if err != nil {
		return fmt.Errorf("failed to create daemon server: %w", err)
	}
	defer srv.Close()

	grpcServer := grpc.NewServer()
	daemonpb.RegisterAgentGateDServer(grpcServer, srv)

	log.InfoContext(context.Background(), "daemon listening", "socket", socketPath)
	return grpcServer.Serve(listener)
}

func daemonListener(socketPath string) (net.Listener, error) {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove stale socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}
	return listener, nil
}
