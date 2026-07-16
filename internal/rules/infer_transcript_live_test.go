package rules

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestFetchTranscriptTailLive proves fetchTranscriptTail returns real transcript
// text from the running clyde daemon and that a larger token budget returns more
// text. It is gated behind AGENT_GATE_CLYDE_LIVE=1 so CI (no clyde) skips it. Set
// AGENT_GATE_CLYDE_SOCKET and AGENT_GATE_CLYDE_CONVERSATION to override the socket
// path and conversation id from `clyde daemon status`.
func TestFetchTranscriptTailLive(t *testing.T) {
	if os.Getenv("AGENT_GATE_CLYDE_LIVE") != "1" {
		t.Skip("set AGENT_GATE_CLYDE_LIVE=1 to run the live clyde transcript test")
	}
	socket := os.Getenv("AGENT_GATE_CLYDE_SOCKET")
	if socket == "" {
		t.Fatal("AGENT_GATE_CLYDE_SOCKET is required for the live test")
	}
	conversationID := os.Getenv("AGENT_GATE_CLYDE_CONVERSATION")
	if conversationID == "" {
		t.Fatal("AGENT_GATE_CLYDE_CONVERSATION is required for the live test")
	}

	runtime := NewInferRuntimeWithCache(nil, nil)
	t.Cleanup(runtime.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	smallText, smallErr := runtime.fetchTranscriptTail(ctx, transcriptParams{
		endpoint:       socket,
		conversationID: conversationID,
		maxTokens:      2000,
	})
	if smallErr != "" {
		t.Fatalf("small-budget fetch error class = %q, want empty", smallErr)
	}
	if smallText == "" {
		t.Fatal("small-budget transcript is empty, want real text")
	}

	largeText, largeErr := runtime.fetchTranscriptTail(ctx, transcriptParams{
		endpoint:       socket,
		conversationID: conversationID,
		maxTokens:      8000,
	})
	if largeErr != "" {
		t.Fatalf("large-budget fetch error class = %q, want empty", largeErr)
	}
	if len(largeText) <= len(smallText) {
		t.Fatalf("large-budget transcript (%d bytes) not longer than small-budget (%d bytes)",
			len(largeText), len(smallText))
	}

	head := smallText
	if len(head) > 400 {
		head = head[:400]
	}
	t.Logf("small-budget length=%d bytes, large-budget length=%d bytes", len(smallText), len(largeText))
	t.Logf("small-budget head:\n%s", head)
}
