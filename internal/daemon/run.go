package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/processlock"
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

	processLock, err := processlock.Acquire(config.RuntimeDir())
	if errors.Is(err, processlock.ErrBusy) {
		log.InfoContext(ctx, "daemon already running")
		return nil
	}
	if err != nil {
		log.ErrorContext(ctx, "open daemon process lock failed", "err", err)
		return fmt.Errorf("open daemon process lock: %w", err)
	}
	defer func() { _ = processLock.Release() }()

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

	if err := serveAfterReadiness(grpcServer, listener, func() {
		log.InfoContext(ctx, "daemon listening", "socket", socketPath)
		srv.StartMaintenanceScheduler(ctx)
	}); err != nil {
		log.ErrorContext(ctx, "grpc serve failed", "err", err)
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

type readinessListener struct {
	net.Listener
	once    sync.Once
	serving chan struct{}
}

type listenerAcceptError struct {
	err error
}

type temporaryError interface {
	error
	Temporary() bool
}

func (err *listenerAcceptError) Error() string { return "accept daemon connection: " + err.err.Error() }
func (err *listenerAcceptError) Unwrap() error { return err.err }
func (err *listenerAcceptError) Timeout() bool {
	var acceptErr net.Error
	return errors.As(err.err, &acceptErr) && acceptErr.Timeout()
}

func (err *listenerAcceptError) Temporary() bool {
	var acceptErr temporaryError
	return errors.As(err.err, &acceptErr) && acceptErr.Temporary()
}

func newReadinessListener(listener net.Listener) *readinessListener {
	return &readinessListener{
		Listener: listener,
		once:     sync.Once{},
		serving:  make(chan struct{}),
	}
}

func (listener *readinessListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.serving) })
	connection, err := listener.Listener.Accept()
	if err != nil {
		return nil, &listenerAcceptError{err: err}
	}
	return connection, nil
}

func (listener *readinessListener) Serving() <-chan struct{} {
	return listener.serving
}

func serveAsync(server *grpc.Server, listener net.Listener) <-chan error {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("grpc serve panic recovered", "err", recovered)
				result <- fmt.Errorf("grpc serve panic: %v", recovered)
			}
		}()
		result <- server.Serve(listener)
	}()
	return result
}

func serveAfterReadiness(server *grpc.Server, listener net.Listener, ready func()) error {
	readyListener := newReadinessListener(listener)
	serveResult := serveAsync(server, readyListener)
	select {
	case <-readyListener.Serving():
		ready()
	case err := <-serveResult:
		select {
		case <-readyListener.Serving():
			ready()
		default:
		}
		return normalizeServeError(err)
	}
	return normalizeServeError(<-serveResult)
}

func normalizeServeError(err error) error {
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
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
