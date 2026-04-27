package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"

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

	socketPath := config.DaemonSocketPath()

	// Remove stale socket from a previous run.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", socketPath, err)
	}

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
