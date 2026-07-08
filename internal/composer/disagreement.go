package composer

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// DisagreementRecord is one append-only retraining-data record.
type DisagreementRecord struct {
	Timestamp       string  `json:"timestamp"`
	RuleSetID       string  `json:"rule_set_id"`
	Command         string  `json:"command"`
	CWD             string  `json:"cwd"`
	OracleVerdict   string  `json:"oracle_verdict"`
	LLMVerdict      string  `json:"llm_verdict"`
	EnforcedVerdict string  `json:"enforced_verdict"`
	Reason          string  `json:"reason"`
	OracleReason    string  `json:"oracle_reason"`
	LLMReason       string  `json:"llm_reason"`
	LLMError        string  `json:"llm_error,omitempty"`
	OracleLatencyMS float64 `json:"oracle_latency_ms"`
	LLMLatencyMS    float64 `json:"llm_latency_ms"`
}

// AppendDisagreement appends one JSONL record. An empty path disables logging.
func AppendDisagreement(path string, record DisagreementRecord) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		slog.Warn("create disagreement log directory failed", "path", path, "err", err)
		return fmt.Errorf("create disagreement log directory: %w", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		slog.Warn("marshal disagreement record failed", "rule_set_id", record.RuleSetID, "err", err)
		return fmt.Errorf("marshal disagreement record: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		slog.Warn("open disagreement log failed", "path", path, "err", err)
		return fmt.Errorf("open disagreement log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append(encoded, '\n')); err != nil {
		slog.Warn("write disagreement record failed", "path", path, "rule_set_id", record.RuleSetID, "err", err)
		return fmt.Errorf("write disagreement record: %w", err)
	}
	return nil
}
