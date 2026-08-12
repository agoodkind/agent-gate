package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type controlledAcceptListener struct {
	addrEntered     chan struct{}
	allowAddr       chan struct{}
	acceptEntered   chan struct{}
	release         chan struct{}
	closed          chan struct{}
	addrEnteredOnce sync.Once
	allowAddrOnce   sync.Once
	releaseOnce     sync.Once
	closeOnce       sync.Once
	acceptCalls     atomic.Int64
	connection      net.Conn
	peer            net.Conn
}

func newControlledAcceptListener() *controlledAcceptListener {
	connection, peer := net.Pipe()
	return &controlledAcceptListener{
		addrEntered:     make(chan struct{}),
		allowAddr:       make(chan struct{}),
		acceptEntered:   make(chan struct{}),
		release:         make(chan struct{}),
		closed:          make(chan struct{}),
		addrEnteredOnce: sync.Once{},
		allowAddrOnce:   sync.Once{},
		releaseOnce:     sync.Once{},
		closeOnce:       sync.Once{},
		acceptCalls:     atomic.Int64{},
		connection:      connection,
		peer:            peer,
	}
}

func (listener *controlledAcceptListener) Accept() (net.Conn, error) {
	if listener.acceptCalls.Add(1) == 1 {
		close(listener.acceptEntered)
		<-listener.release
		return listener.connection, nil
	}
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *controlledAcceptListener) Close() error {
	listener.closeOnce.Do(func() {
		listener.allowAddrOnce.Do(func() { close(listener.allowAddr) })
		listener.releaseOnce.Do(func() { close(listener.release) })
		close(listener.closed)
		_ = listener.connection.Close()
		_ = listener.peer.Close()
	})
	return nil
}

func (listener *controlledAcceptListener) Addr() net.Addr {
	listener.addrEnteredOnce.Do(func() { close(listener.addrEntered) })
	<-listener.allowAddr
	return controlledAddr("controlled")
}

type controlledAddr string

func (addr controlledAddr) Network() string { return string(addr) }
func (addr controlledAddr) String() string  { return string(addr) }

func TestDaemonListenerCreatesSocketPath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agent-gate-listener-test.")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "daemon.sock")
	lis, err := daemonListener(t.Context(), socketPath)
	if err != nil {
		t.Fatalf("daemonListener: %v", err)
	}
	if err := lis.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat socket path: %v", err)
	}
}

func TestMaintenanceSchedulerStartsAfterServeAcceptLoop(t *testing.T) {
	listener := newControlledAcceptListener()
	server := grpc.NewServer()
	started := make(chan struct{})
	serveDone := make(chan error, 1)

	go func() {
		serveDone <- serveAfterReadiness(server, listener, func() { close(started) })
	}()
	<-listener.addrEntered
	select {
	case <-started:
		t.Fatal("maintenance scheduler started before Serve reached Accept")
	default:
	}
	listener.allowAddrOnce.Do(func() { close(listener.allowAddr) })
	<-listener.acceptEntered
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("maintenance scheduler did not start after Serve entered Accept")
	}
	server.Stop()
	if err := <-serveDone; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("serveAfterReadiness: %v", err)
	}
}

var errFatalAccept = errors.New("fatal accept failure")

type fatalAcceptListener struct{}

func (*fatalAcceptListener) Accept() (net.Conn, error) { return nil, errFatalAccept }
func (*fatalAcceptListener) Close() error              { return nil }
func (*fatalAcceptListener) Addr() net.Addr            { return controlledAddr("fatal") }

func TestFatalFirstAcceptReportsAcceptLoopEntry(t *testing.T) {
	for i := 0; i < 100; i++ {
		server := grpc.NewServer()
		var ready atomic.Bool
		err := serveAfterReadiness(server, &fatalAcceptListener{}, func() { ready.Store(true) })
		if !errors.Is(err, errFatalAccept) {
			t.Fatalf("iteration %d: Serve error = %v", i, err)
		}
		if !ready.Load() {
			t.Fatalf("iteration %d: readiness not reported after first Accept entry", i)
		}
	}
}

func TestServeFailureBeforeAcceptDoesNotReportReady(t *testing.T) {
	listener := newControlledAcceptListener()
	server := grpc.NewServer()
	server.Stop()
	var ready atomic.Bool
	if err := serveAfterReadiness(server, listener, func() { ready.Store(true) }); err != nil {
		t.Fatalf("stopped Serve returned %v", err)
	}
	if ready.Load() {
		t.Fatal("readiness reported before Accept")
	}
	select {
	case <-listener.acceptEntered:
		t.Fatal("stopped Serve entered Accept")
	default:
	}
}

type temporaryAcceptError struct{}

func (temporaryAcceptError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptError) Temporary() bool { return true }
func (temporaryAcceptError) Timeout() bool   { return false }

type temporaryThenBlockingListener struct {
	calls  atomic.Int64
	second chan struct{}
	closed chan struct{}
}

func (listener *temporaryThenBlockingListener) Accept() (net.Conn, error) {
	if listener.calls.Add(1) == 1 {
		return nil, temporaryAcceptError{}
	}
	close(listener.second)
	<-listener.closed
	return nil, net.ErrClosed
}

func (listener *temporaryThenBlockingListener) Close() error {
	select {
	case <-listener.closed:
	default:
		close(listener.closed)
	}
	return nil
}

func (*temporaryThenBlockingListener) Addr() net.Addr { return controlledAddr("temporary") }

func TestReadinessPreservesTemporaryAcceptRetry(t *testing.T) {
	listener := &temporaryThenBlockingListener{
		second: make(chan struct{}),
		closed: make(chan struct{}),
	}
	server := grpc.NewServer()
	var ready atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- serveAfterReadiness(server, listener, func() { ready.Store(true) })
	}()
	select {
	case <-listener.second:
		if !ready.Load() {
			t.Fatal("readiness not reported after temporary Accept entered")
		}
		server.Stop()
	case err := <-done:
		t.Fatalf("Serve exited after temporary accept error: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Serve did not retry temporary accept error")
	}
	if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("serveAfterReadiness: %v", err)
	}
}
