package hook

import (
	"encoding/json"
	"os"
	"path/filepath"

	"goodkind.io/agent-gate/internal/config"
)

// followupDir returns the directory where pending followup flags are stored.
func followupDir() string {
	return filepath.Join(config.RuntimeDir(), "followups")
}

// followupPath returns the flag file path for a given session (conversation) ID.
func followupPath(sessionID string) string {
	return filepath.Join(followupDir(), sessionID+".json")
}

// followupData is the JSON written to a flag file.
type followupData struct {
	RuleName string `json:"rule_name"`
	Message  string `json:"message"`
}

// writeFollowup persists a violation so a later stop hook can send a followup_message.
func writeFollowup(sessionID, ruleName, message string) error {
	dir := followupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(followupData{RuleName: ruleName, Message: message})
	if err != nil {
		return err
	}
	return os.WriteFile(followupPath(sessionID), data, 0o600)
}

// consumeFollowup reads and deletes a pending followup flag for the given session.
// Returns empty strings if no flag is set.
func consumeFollowup(sessionID string) (ruleName, message string) {
	path := followupPath(sessionID)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	_ = os.Remove(path)
	var d followupData
	if json.Unmarshal(raw, &d) != nil {
		return "", ""
	}
	return d.RuleName, d.Message
}
