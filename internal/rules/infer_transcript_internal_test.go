package rules

import (
	"context"
	"errors"
	"io"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
)

// fakeExportStream implements the server-streaming client for ExportChunk by
// returning a fixed chunk sequence, then io.EOF, or a fixed Recv error.
type fakeExportStream struct {
	grpc.ServerStreamingClient[clydev1.ExportChunk]
	chunks  []*clydev1.ExportChunk
	index   int
	recvErr error
}

func (stream *fakeExportStream) Recv() (*clydev1.ExportChunk, error) {
	if stream.recvErr != nil {
		return nil, stream.recvErr
	}
	if stream.index >= len(stream.chunks) {
		return nil, io.EOF
	}
	chunk := stream.chunks[stream.index]
	stream.index++
	return chunk, nil
}

// fakeClydeServiceClient records the last export request and returns a fixed
// stream or a fixed stream-open error.
type fakeClydeServiceClient struct {
	clydev1.ClydeServiceClient
	lastRequest *clydev1.ExportTranscriptRequest
	stream      grpc.ServerStreamingClient[clydev1.ExportChunk]
	streamErr   error
}

func (client *fakeClydeServiceClient) StreamExportTranscript(
	_ context.Context,
	in *clydev1.ExportTranscriptRequest,
	_ ...grpc.CallOption,
) (grpc.ServerStreamingClient[clydev1.ExportChunk], error) {
	client.lastRequest = in
	if client.streamErr != nil {
		return nil, client.streamErr
	}
	return client.stream, nil
}

func chunk(body string) *clydev1.ExportChunk {
	return &clydev1.ExportChunk{Body: []byte(body)}
}

func TestFetchTranscriptTailConcatenatesChunks(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	client := &fakeClydeServiceClient{
		stream: &fakeExportStream{chunks: []*clydev1.ExportChunk{chunk("first "), chunk("second")}},
	}
	runtime.clydeServiceClients["/socket"] = client

	text, errClass := runtime.fetchTranscriptTail(context.Background(), transcriptParams{
		endpoint:       "/socket",
		conversationID: "b2b01aab",
		maxTokens:      2000,
	})

	if errClass != "" {
		t.Fatalf("error class = %q, want empty", errClass)
	}
	if text != "first second" {
		t.Fatalf("text = %q, want %q", text, "first second")
	}
	if client.lastRequest.GetConversationId() != "b2b01aab" {
		t.Fatalf("conversation id = %q, want %q", client.lastRequest.GetConversationId(), "b2b01aab")
	}
	if client.lastRequest.GetMaxTokens() != "2000" {
		t.Fatalf("max tokens = %q, want %q", client.lastRequest.GetMaxTokens(), "2000")
	}
	if !client.lastRequest.GetIncludeChat() || !client.lastRequest.GetIncludeToolCalls() {
		t.Fatalf("include_chat/include_tool_calls = %v/%v, want true/true",
			client.lastRequest.GetIncludeChat(), client.lastRequest.GetIncludeToolCalls())
	}
	if client.lastRequest.GetFormat() != "plain_text" || client.lastRequest.GetWhitespace() != "compact" {
		t.Fatalf("format/whitespace = %q/%q, want plain_text/compact",
			client.lastRequest.GetFormat(), client.lastRequest.GetWhitespace())
	}
}

// TestTranscriptClientReusesContextConnection confirms transcriptClient builds its
// ClydeService client over the *grpc.ClientConn already cached in contextConnections
// for the endpoint, rather than opening a second dial, so the transcript read and
// the conversation-context read share one connection. Pre-seeding the sentinel
// connection and asserting it survives unchanged proves the reuse path took over
// from a fresh dial.
func TestTranscriptClientReusesContextConnection(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)

	sentinel, err := grpc.NewClient(
		"passthrough:///transcript-reuse",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("build sentinel connection: %v", err)
	}
	runtime.contextConnections["/socket"] = sentinel

	client, err := runtime.transcriptClient("/socket")
	if err != nil {
		t.Fatalf("transcriptClient error: %v", err)
	}
	if client == nil {
		t.Fatal("transcriptClient returned a nil client")
	}
	if runtime.contextConnections["/socket"] != sentinel {
		t.Fatal("transcriptClient replaced the cached connection, want the reused sentinel")
	}
	if runtime.clydeServiceClients["/socket"] == nil {
		t.Fatal("transcriptClient did not cache a ClydeService client over the reused connection")
	}
}

func TestFetchTranscriptTailStreamErrorYieldsErrorClass(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	client := &fakeClydeServiceClient{streamErr: errors.New("stream open failed")}
	runtime.clydeServiceClients["/socket"] = client

	text, errClass := runtime.fetchTranscriptTail(context.Background(), transcriptParams{
		endpoint:       "/socket",
		conversationID: "b2b01aab",
		maxTokens:      2000,
	})

	if text != "" {
		t.Fatalf("text = %q, want empty on stream error", text)
	}
	if errClass == "" {
		t.Fatalf("error class empty, want non-empty on stream error")
	}
}

func TestFetchTranscriptTailRecvErrorYieldsErrorClass(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	client := &fakeClydeServiceClient{
		stream: &fakeExportStream{recvErr: errors.New("recv failed")},
	}
	runtime.clydeServiceClients["/socket"] = client

	text, errClass := runtime.fetchTranscriptTail(context.Background(), transcriptParams{
		endpoint:       "/socket",
		conversationID: "b2b01aab",
		maxTokens:      2000,
	})

	if text != "" {
		t.Fatalf("text = %q, want empty on recv error", text)
	}
	if errClass == "" {
		t.Fatalf("error class empty, want non-empty on recv error")
	}
}

func TestFetchTranscriptTailInvalidBudgetYieldsErrorClass(t *testing.T) {
	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)
	client := &fakeClydeServiceClient{
		stream: &fakeExportStream{chunks: []*clydev1.ExportChunk{chunk("body")}},
	}
	runtime.clydeServiceClients["/socket"] = client

	text, errClass := runtime.fetchTranscriptTail(context.Background(), transcriptParams{
		endpoint:       "/socket",
		conversationID: "b2b01aab",
		maxTokens:      0,
	})

	if text != "" {
		t.Fatalf("text = %q, want empty on invalid budget", text)
	}
	if errClass == "" {
		t.Fatalf("error class empty, want non-empty on invalid budget")
	}
}
