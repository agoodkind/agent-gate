package audit

import (
	"log/slog"

	"goodkind.io/agent-gate/internal/config"
)

// EntryNormalizer captures an audit entry without writing it.
type EntryNormalizer interface {
	Normalize(system, sessionID, eventName, level, msg string, attrs Attrs) NormalizedEntry
}

// Normalizer retains immutable audit settings without queues or background work.
type Normalizer struct {
	minLevel slog.Level
	enabled  bool
	rawHash  bool
}

// NewNormalizer resolves the settings used by audit writers.
func NewNormalizer(cfg *config.Config) Normalizer {
	return Normalizer{
		minLevel: parseLevel(cfg.AuditLevel()),
		enabled:  cfg.AuditEnabled(),
		rawHash:  true,
	}
}

// Normalize captures the event identity, timestamp, fields, and payload hash.
func (normalizer Normalizer) Normalize(
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	attrs Attrs,
) NormalizedEntry {
	if !normalizer.enabled || parseLevel(level) < normalizer.minLevel {
		return emptyNormalizedEntry()
	}

	event := normalizeEvent(system, sessionID, eventName, level, msg, attrs)
	fingerprint := dedupFingerprint(event, attrs)
	rawPayload := ""
	if value, ok := attrs["raw_payload"]; ok {
		rawPayload = value.String()
	}

	if rawPayload != "" && normalizer.rawHash {
		event.RawPayloadHash = payloadHash(rawPayload)
	}

	event.EventID = "evt_" + fingerprint[:32]

	return NormalizedEntry{Event: event, Fingerprint: fingerprint}
}
