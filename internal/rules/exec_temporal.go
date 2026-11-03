package rules

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"goodkind.io/agent-gate/internal/hotkv"
)

const execTemporalNamespace = hotkv.InternalNamespacePrefix + "exec-temporal"

type execTemporalStore struct {
	cache *hotkv.Store
}

type execResponseTargetKey struct{}

// WithExecResponseTargetResolver records an action-aware model-facing target
// resolver for response rules evaluated under ctx.
func WithExecResponseTargetResolver(
	ctx context.Context,
	resolve func(string) string,
) context.Context {
	return context.WithValue(ctx, execResponseTargetKey{}, resolve)
}

// ObserveUserPrompt records a prompt in the standalone receipt sequence.
func (r *ExecRuntime) ObserveUserPrompt(
	system string,
	fields FieldSet,
	receiptID int64,
	value string,
) bool {
	return r.ObserveUserPromptOrdered(system, fields, hotkv.ObservationOrder{Generation: 0, ReceiptID: receiptID}, value)
}

// ObserveUserPromptOrdered records a typed prompt with its original assignment.
func (r *ExecRuntime) ObserveUserPromptOrdered(system string, fields FieldSet, order hotkv.ObservationOrder, value string) bool {
	if r == nil || r.temporal == nil || value == "" {
		return false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" {
		return false
	}
	return r.temporal.store(temporalKey("prompt", identity), order, value)
}

// ObserveResponseOutput records the final model-facing response for an event
// target in the standalone receipt sequence. Action remains accepted for callers that
// retain response-effect metadata, but it does not identify temporal state.
func (r *ExecRuntime) ObserveResponseOutput(
	system string,
	fields FieldSet,
	eventName string,
	_ string,
	target string,
	receiptID int64,
	value string,
) bool {
	return r.ObserveResponseOutputOrdered(system, fields, eventName, target, hotkv.ObservationOrder{Generation: 0, ReceiptID: receiptID}, value)
}

// ObserveResponseOutputOrdered records a response with its original assignment.
func (r *ExecRuntime) ObserveResponseOutputOrdered(system string, fields FieldSet, eventName, target string, order hotkv.ObservationOrder, value string) bool {
	if r == nil || r.temporal == nil || value == "" {
		return false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" || eventName == "" || target == "" {
		return false
	}
	key := temporalKey("response", identity, eventName, target)
	return r.temporal.store(key, order, value)
}

func temporalConversationIdentity(system string, fields FieldSet) string {
	conversationID := fields.ConversationID
	if conversationID == "" {
		conversationID = fields.SessionID
	}
	if system == "" || conversationID == "" {
		return ""
	}
	return temporalKey(system, conversationID)
}

func temporalKey(parts ...string) string {
	hash := sha256.New()
	var lengthBytes [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(lengthBytes[:], uint64(len(part)))
		_, _ = hash.Write(lengthBytes[:])
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (s *execTemporalStore) store(key string, order hotkv.ObservationOrder, value string) bool {
	if s == nil || s.cache == nil || key == "" || order.ReceiptID <= 0 {
		return false
	}
	stored, err := s.cache.Observe(execTemporalNamespace, key, order, value)
	return err == nil && stored
}
