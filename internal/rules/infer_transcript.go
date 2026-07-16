package rules

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
)

// transcriptParams carries the inputs a transcript-tail fetch needs. maxTokens is
// the token budget for the tail; clyde's max_tokens field takes a size string, so
// the fetch renders it as a decimal string. tokenModel may be empty, which lets
// clyde derive the tokenizer from the conversation's provider.
type transcriptParams struct {
	endpoint       string
	conversationID string
	tokenModel     string
	maxTokens      int
}

// fetchTranscriptTail returns the last maxTokens tokens of the conversation's
// rendered transcript (chat + tool calls) from clyde's StreamExportTranscript. It
// returns (text, errorClass); a transport, budget, or stream failure yields "" and
// a non-empty error class the caller maps to its on-error policy (fail-open). The
// reply is a server stream, so it reads chunks until [io.EOF] and concatenates
// each chunk body in receive order.
func (runtime *InferRuntime) fetchTranscriptTail(ctx context.Context, params transcriptParams) (string, string) {
	if params.maxTokens <= 0 {
		return "", "transcript_invalid"
	}
	client, err := runtime.transcriptClient(params.endpoint)
	if err != nil {
		return "", "transcript_unavailable"
	}
	stream, err := client.StreamExportTranscript(ctx, &clydev1.ExportTranscriptRequest{
		ConversationId:   params.conversationID,
		MaxTokens:        strconv.Itoa(params.maxTokens),
		TokenModel:       params.tokenModel,
		Format:           "plain_text",
		Whitespace:       "compact",
		IncludeChat:      true,
		IncludeToolCalls: true,
	})
	if err != nil {
		return "", "transcript_unavailable"
	}
	var builder strings.Builder
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return "", "transcript_unavailable"
		}
		builder.Write(chunk.GetBody())
	}
	return builder.String(), ""
}

// transcriptClient dials and caches the clyde ClydeService gRPC client for an
// endpoint, reusing the connection contextClient already cached for that endpoint
// so the transcript and conversation-context reads share one *grpc.ClientConn.
func (runtime *InferRuntime) transcriptClient(endpoint string) (clydev1.ClydeServiceClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if client := runtime.clydeServiceClients[endpoint]; client != nil {
		return client, nil
	}
	connection := runtime.contextConnections[endpoint]
	if connection == nil {
		dialed, err := grpc.NewClient(grpcEndpoint(endpoint), grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			runtime.log.Warn("create transcript client failed", "endpoint", endpoint, "err", err)
			return nil, fmt.Errorf("create transcript client: %w", err)
		}
		connection = dialed
		runtime.contextConnections[endpoint] = connection
	}
	client := clydev1.NewClydeServiceClient(connection)
	runtime.clydeServiceClients[endpoint] = client
	return client, nil
}
