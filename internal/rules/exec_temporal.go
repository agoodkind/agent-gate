package rules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	"goodkind.io/agent-gate/internal/hotkv"
)

const execTemporalNamespace = hotkv.InternalNamespacePrefix + "exec-temporal"

const temporalRecordHeaderBytes = 9

var execTemporalReceiptMu sync.Mutex

type execTemporalStore struct {
	cache     *hotkv.Store
	afterRead func()
}

type temporalRecord struct {
	receiptID int64
	available bool
	value     string
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

// ObserveUserPrompt records a typed user prompt using durable receipt order.
func (r *ExecRuntime) ObserveUserPrompt(
	system string,
	fields FieldSet,
	receiptID int64,
	value string,
) bool {
	if r == nil || r.temporal == nil || value == "" {
		return false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" {
		return false
	}
	return r.temporal.store(temporalKey("prompt", identity), receiptID, value)
}

// ObserveResponseOutput records the final model-facing response for an event
// target using durable receipt order. Action remains accepted for callers that
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
	if r == nil || r.temporal == nil || value == "" {
		return false
	}
	identity := temporalConversationIdentity(system, fields)
	if identity == "" || eventName == "" || target == "" {
		return false
	}
	key := temporalKey("response", identity, eventName, target)
	return r.temporal.store(key, receiptID, value)
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

func (s *execTemporalStore) store(key string, receiptID int64, value string) bool {
	if s == nil || s.cache == nil || key == "" || receiptID <= 0 {
		return false
	}
	execTemporalReceiptMu.Lock()
	defer execTemporalReceiptMu.Unlock()

	entry, found, err := s.cache.Get(execTemporalNamespace, key)
	if err != nil {
		return false
	}
	if found {
		currentReceipt, _, _, valid := decodeTemporalRecord(entry.Value)
		if valid && currentReceipt >= receiptID {
			return false
		}
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	record, err := (temporalRecord{
		receiptID: receiptID,
		available: true,
		value:     value,
	}).MarshalBinary()
	if err != nil {
		return false
	}
	if _, _, err := s.cache.Set(
		execTemporalNamespace,
		key,
		record,
		hotkv.SetOptions{Mode: hotkv.SetModeAny, TTL: 0},
	); err == nil {
		return true
	} else if !errors.Is(err, hotkv.ErrValueTooLarge) {
		return false
	}
	tombstone, err := (temporalRecord{
		receiptID: receiptID,
		available: false,
		value:     "",
	}).MarshalBinary()
	if err != nil {
		return false
	}
	_, _, err = s.cache.Set(
		execTemporalNamespace,
		key,
		tombstone,
		hotkv.SetOptions{Mode: hotkv.SetModeAny, TTL: 0},
	)
	return err == nil
}

func (temporal temporalRecord) MarshalBinary() ([]byte, error) {
	maxInt := int(^uint(0) >> 1)
	if len(temporal.value) > maxInt-temporalRecordHeaderBytes {
		return nil, errors.New("encode temporal record: value too large")
	}
	var receiptBuffer bytes.Buffer
	if err := binary.Write(&receiptBuffer, binary.BigEndian, temporal.receiptID); err != nil {
		return nil, fmt.Errorf("encode temporal receipt: %w", err)
	}
	record := make([]byte, temporalRecordHeaderBytes+len(temporal.value))
	copy(record[:8], receiptBuffer.Bytes())
	if temporal.available {
		record[8] = 1
	}
	copy(record[temporalRecordHeaderBytes:], temporal.value)
	return record, nil
}

func decodeTemporalRecord(record []byte) (int64, bool, string, bool) {
	if len(record) < temporalRecordHeaderBytes {
		return 0, false, "", false
	}
	receiptValue := binary.BigEndian.Uint64(record[:8])
	if receiptValue > ^uint64(0)>>1 {
		return 0, false, "", false
	}
	receiptID := int64(receiptValue)
	return receiptID, record[8] == 1, string(record[temporalRecordHeaderBytes:]), true
}
