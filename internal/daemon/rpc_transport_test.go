package daemon_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/hook"
)

const rpcMiB = 1024 * 1024

type rpcHarness struct{ client daemonpb.AgentGateDClient }

func newRPCHarness(t *testing.T, concurrency, wait int) rpcHarness {
	t.Helper()
	directory, directoryErr := os.MkdirTemp("", "ag-")
	if directoryErr != nil {
		t.Fatal(directoryErr)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(directory, "runtime"))
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("[performance.hook]\nhot_concurrency = %d\nhot_queue_wait_ms = %d\n", concurrency, wait)
	body += fmt.Sprintf("[performance.hook.cache]\nmax_value_bytes = %d\n", daemon.RPCMaxMessageBytes)
	body += "[[rules]]\nname = 'rpc-block'\ncodex_events = ['PreToolUse']\nclaude_events = ['PreToolUse']\nfield_paths = ['tool_input.command']\npattern = 'forbidden'\naction = 'block'\nviolation_message = 'blocked command'\n"
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	server, err := daemon.New(t.Context(), slog.New(slog.NewTextHandler(io.Discard, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if err := config.EnsureRuntimeDir(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", config.DaemonSocketPath())
	if err != nil {
		t.Fatal(err)
	}
	transport := server.GRPCServer()
	done := make(chan error, 1)
	go func() { done <- transport.Serve(listener) }()
	t.Cleanup(func() { transport.Stop(); <-done })
	server.StartAuditScheduler(t.Context())
	connection, err := grpc.NewClient("unix://"+config.DaemonSocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2*daemon.RPCMaxMessageBytes)),
		grpc.WithStaticStreamWindowSize(65535), grpc.WithStaticConnWindowSize(65535))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := daemonpb.NewAgentGateDClient(connection)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := client.Status(ctx, &daemonpb.StatusRequest{}); err != nil {
		t.Fatal(err)
	}
	return rpcHarness{client: client}
}

func rpcPayload(session, command string) []byte {
	return []byte(fmt.Sprintf(`{"session_id":%q,"hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":%q}}`, session, command))
}

func TestRPCKVBoundsAndMetadataOnly(t *testing.T) {
	harness := newRPCHarness(t, 4, 25)
	value := bytes.Repeat([]byte("v"), rpcMiB)
	for index := range 5 {
		response, err := harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "rpc", Key: fmt.Sprint(index), Value: value})
		if err != nil || !response.GetStored() || !bytes.Equal(response.GetEntry().GetValue(), value) {
			t.Fatalf("set = %v", err)
		}
	}
	before := runtime.MemStats{}
	runtime.ReadMemStats(&before)
	response, err := harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "rpc"})
	after := runtime.MemStats{}
	runtime.ReadMemStats(&after)
	if err != nil || len(response.GetEntries()) != 5 {
		t.Fatalf("metadata list = %v, %v", response, err)
	}
	for _, entry := range response.GetEntries() {
		if len(entry.GetValue()) != 0 {
			t.Fatal("metadata list returned values")
		}
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= uint64(len(value)) {
		t.Fatalf("metadata list allocated %d bytes for five 1 MiB values", allocated)
	}
	before = after
	_, err = harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "rpc", IncludeValues: true})
	runtime.ReadMemStats(&after)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized list = %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated >= uint64(len(value)) {
		t.Fatalf("rejected list allocated %d bytes", allocated)
	}
	response, err = harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "rpc", IncludeValues: true, Limit: 3})
	if err != nil || len(response.GetEntries()) != 3 {
		t.Fatalf("bounded list = %v", err)
	}
	for _, entry := range response.GetEntries() {
		if !bytes.Equal(entry.GetValue(), value) {
			t.Fatal("list changed a value")
		}
	}
	deleted, err := harness.client.KVGetDelete(t.Context(), &daemonpb.KVGetDeleteRequest{Namespace: "rpc", Key: "0"})
	if err != nil || !deleted.GetFound() || !bytes.Equal(deleted.GetEntry().GetValue(), value) {
		t.Fatalf("get-delete = %v", err)
	}
	got, err := harness.client.KVGet(t.Context(), &daemonpb.KVGetRequest{Namespace: "rpc", Key: "0"})
	if err != nil || got.GetFound() {
		t.Fatalf("deleted entry = %v, %v", got, err)
	}
	_, err = harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "rpc", Key: "oversized", Value: make([]byte, daemon.RPCMaxMessageBytes)})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("oversized receive = %v", err)
	}
}

func TestRPCSingleKVResponseAboveDefaultReceiveLimit(t *testing.T) {
	harness := newRPCHarness(t, 4, 25)
	request := &daemonpb.KVSetRequest{Namespace: "rpc", Key: "boundary", Value: bytes.Repeat([]byte("v"), daemon.RPCMaxMessageBytes)}
	for proto.Size(request) > daemon.RPCMaxMessageBytes {
		request.Value = request.Value[:len(request.Value)-1]
	}
	stored, err := harness.client.KVSet(t.Context(), request)
	if err != nil || !stored.GetStored() {
		t.Fatalf("set near receive limit = %v", err)
	}
	got, err := harness.client.KVGet(t.Context(), &daemonpb.KVGetRequest{Namespace: request.Namespace, Key: request.Key})
	if err != nil || !got.GetFound() || !bytes.Equal(got.GetEntry().GetValue(), request.Value) {
		t.Fatalf("get near receive limit = %v", err)
	}
	if proto.Size(got) <= daemon.RPCMaxMessageBytes {
		t.Fatalf("get response size %d does not exceed the default receive limit", proto.Size(got))
	}
	deleted, err := harness.client.KVGetDelete(t.Context(), &daemonpb.KVGetDeleteRequest{Namespace: request.Namespace, Key: request.Key})
	if err != nil || !deleted.GetFound() || !bytes.Equal(deleted.GetEntry().GetValue(), request.Value) {
		t.Fatalf("get-delete near receive limit = %v", err)
	}
	if proto.Size(deleted) <= daemon.RPCMaxMessageBytes {
		t.Fatalf("get-delete response size %d does not exceed the default receive limit", proto.Size(deleted))
	}
}

func TestRPCKVListExactMessageLimit(t *testing.T) {
	harness := newRPCHarness(t, 4, 25)
	value := bytes.Repeat([]byte("v"), 2*rpcMiB)
	first, err := harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "boundary", Key: "a", Value: value})
	if err != nil || !first.GetStored() {
		t.Fatalf("set first entry: %v", err)
	}
	second, err := harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "boundary", Key: "b", Value: value})
	if err != nil || !second.GetStored() {
		t.Fatalf("set second entry: %v", err)
	}
	candidate := &daemonpb.KVListResponse{Entries: []*daemonpb.KVEntry{first.GetEntry(), second.GetEntry()}}
	for attempt := 0; attempt < 4 && proto.Size(candidate) != daemon.RPCMaxMessageBytes; attempt++ {
		length := len(value) + daemon.RPCMaxMessageBytes - proto.Size(candidate)
		value = bytes.Repeat([]byte("v"), length)
		second, err = harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "boundary", Key: "b", Value: value})
		if err != nil || !second.GetStored() {
			t.Fatalf("set boundary entry: %v", err)
		}
		candidate.Entries[1] = second.GetEntry()
	}
	if size := proto.Size(candidate); size != daemon.RPCMaxMessageBytes {
		t.Fatalf("fixture size = %d, want %d", size, daemon.RPCMaxMessageBytes)
	}
	listed, err := harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "boundary", IncludeValues: true})
	if err != nil || !proto.Equal(listed, candidate) {
		t.Fatalf("list at limit: %v", err)
	}
	value = append(value, 'v')
	second, err = harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{Namespace: "boundary", Key: "b", Value: value})
	if err != nil || !second.GetStored() {
		t.Fatalf("set entry above limit: %v", err)
	}
	candidate.Entries[1] = second.GetEntry()
	if size := proto.Size(candidate); size != daemon.RPCMaxMessageBytes+1 {
		t.Fatalf("fixture size = %d, want %d", size, daemon.RPCMaxMessageBytes+1)
	}
	_, err = harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "boundary", IncludeValues: true})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("list above limit: %v", err)
	}
}

func TestRPCKVListExpiringEntry(t *testing.T) {
	harness := newRPCHarness(t, 4, 25)
	stored, err := harness.client.KVSet(t.Context(), &daemonpb.KVSetRequest{
		Namespace: "expiry", Key: "entry", Value: []byte("value"), TtlMs: 20000,
	})
	if err != nil || !stored.GetStored() {
		t.Fatalf("set expiring entry: %v", err)
	}
	response, err := harness.client.KVList(t.Context(), &daemonpb.KVListRequest{Namespace: "expiry", IncludeValues: true})
	if err != nil || len(response.GetEntries()) != 1 {
		t.Fatalf("list expiring entry: %v", err)
	}
	entry := response.GetEntries()[0]
	if !bytes.Equal(entry.GetValue(), []byte("value")) || entry.GetPttlMs() <= 0 || entry.GetPttlMs() > 20000 || entry.GetExpiresUnixNano() != stored.GetEntry().GetExpiresUnixNano() {
		t.Fatalf("expiring entry = %v", entry)
	}
}

func TestRPCConcurrentChats(t *testing.T) {
	newRPCHarness(t, 4, 2000)
	client, err := daemon.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	const chats = 12
	errors := make(chan error, chats)
	var group sync.WaitGroup
	for index := range chats {
		group.Go(func() {
			payload := rpcPayload(fmt.Sprint(index), "forbidden")
			if _, err := client.ResolveHookEnvironment(payload, "codex", nil, nil, hook.InvocationContext{}); err != nil {
				errors <- err
				return
			}
			response, err := client.EvaluateHook(payload, "codex", "", nil, nil, hook.InvocationContext{})
			if err == nil && !bytes.Contains(response.GetStdoutData(), []byte("rpc-block")) {
				err = fmt.Errorf("chat %d did not enforce its rule", index)
			}
			errors <- err
		})
	}
	group.Wait()
	for range chats {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
}
