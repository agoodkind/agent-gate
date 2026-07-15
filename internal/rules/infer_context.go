package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/clyde/api/contextpb"
)

// contextParams carries the inputs a conversation-context fetch needs, so both
// the infer-condition path and the batch planner can request context without a
// *config.Condition.
type contextParams struct {
	endpoint        string
	workspace       string
	session         string
	turnBudget      int
	maxCharsPerTurn int
}

// contextJSON fetches recent conversation turns for one infer condition. It
// returns empty context when the condition sets no source, and otherwise
// delegates to fetchContextJSON so the condition path and the batch planner share
// one fetch-and-render implementation.
func (runtime *InferRuntime) contextJSON(
	ctx context.Context,
	condition *config.Condition,
	contextWorkspace string,
	contextSession string,
) (string, string) {
	if condition.ContextSource == "" {
		return "", ""
	}
	return runtime.fetchContextJSON(ctx, contextParams{
		endpoint:        condition.ContextEndpoint,
		workspace:       contextWorkspace,
		session:         contextSession,
		turnBudget:      condition.ContextTurnBudget,
		maxCharsPerTurn: condition.ContextMaxCharsPerTurn,
	})
}

// fetchContextJSON fetches recent conversation turns from the context service and
// renders them as {"turns":[...]} JSON. It returns (json, errorClass); a transport
// or bounds failure yields an empty json and a non-empty error class the caller
// maps to its on-error policy.
func (runtime *InferRuntime) fetchContextJSON(ctx context.Context, params contextParams) (string, string) {
	client, err := runtime.contextClient(params.endpoint)
	if err != nil {
		return "", "context_unavailable"
	}
	turnBudget, turnBudgetValid := checkedInt32(params.turnBudget)
	maxCharsPerTurn, maxCharsValid := checkedInt32(params.maxCharsPerTurn)
	if !turnBudgetValid || !maxCharsValid {
		return "", "context_invalid"
	}
	reply, err := client.GetRecentTurns(ctx, &contextpb.GetRecentTurnsRequest{
		WorkspaceRef:    params.workspace,
		SessionRef:      params.session,
		TurnBudget:      turnBudget,
		MaxCharsPerTurn: maxCharsPerTurn,
	})
	if err != nil || reply == nil {
		return "", "context_unavailable"
	}
	return renderContextTurns(reply.GetTurns())
}

// renderContextTurns marshals conversation turns as {"turns":[...]} JSON, dropping
// turns with no meaningful text so the judge budget spends on real requests and
// replies rather than the tool-only and interruption turns that dominate a busy
// session.
func renderContextTurns(turns []*contextpb.Turn) (string, string) {
	type opaqueTurn struct {
		Role string `json:"role"`
		Text string `json:"text"`
		TS   string `json:"ts"`
	}
	type opaqueContext struct {
		Turns []opaqueTurn `json:"turns"`
	}
	value := opaqueContext{Turns: make([]opaqueTurn, 0, len(turns))}
	for _, turn := range turns {
		if turn == nil {
			continue
		}
		text := turn.GetText()
		if !meaningfulContextText(text) {
			continue
		}
		value.Turns = append(value.Turns, opaqueTurn{Role: turn.GetRole(), Text: text, TS: turn.GetTs()})
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "context_invalid"
	}
	return string(encoded), ""
}

// meaningfulContextText reports whether a turn carries content worth sending to
// the judge. Empty-text turns (a tool-only assistant turn) and interruption
// markers carry no intent, so they are dropped.
func meaningfulContextText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "[Request interrupted") {
		return false
	}
	return true
}

// contextClient dials and caches the conversation-context gRPC client for an
// endpoint. A leading-slash endpoint is dialed as a unix socket by grpcEndpoint.
func (runtime *InferRuntime) contextClient(endpoint string) (contextpb.ConversationContextClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if client := runtime.contextClients[endpoint]; client != nil {
		return client, nil
	}
	connection, err := grpc.NewClient(grpcEndpoint(endpoint), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		runtime.log.Warn("create context client failed", "endpoint", endpoint, "err", err)
		return nil, fmt.Errorf("create context client: %w", err)
	}
	runtime.contextConnections[endpoint] = connection
	client := contextpb.NewConversationContextClient(connection)
	runtime.contextClients[endpoint] = client
	return client, nil
}
