package hook

import (
	"encoding/json"
	"fmt"
)

// FailOpenNoticeText is the warning shown when agent-gate let a tool call
// through without evaluating it. It names the consequence first, because the
// reader needs to know enforcement did not happen before they care why.
func FailOpenNoticeText(reason FailOpenReason, diagnostic string) string {
	if reason == "" {
		return ""
	}
	notice := "agent-gate did not evaluate this call, so no rule was enforced on it (" +
		string(reason) + ")."
	if diagnostic != "" {
		notice += " " + diagnostic
	}
	return notice
}

// claudeFailOpenPayload is the allow response carrying a visible warning.
// systemMessage is surfaced to the user by Claude Code without blocking, which
// is the behavior a fail-open needs: the call proceeds, and the operator learns
// the gate was down instead of the outage passing unnoticed.
type claudeFailOpenPayload struct {
	SystemMessage string `json:"systemMessage"`
}

// claudeFailOpenStdout renders an allow that says enforcement did not happen.
// It falls back to the plain allow when the notice cannot be encoded, because a
// hook that cannot emit JSON must still not break the tool call.
func claudeFailOpenStdout(reason FailOpenReason, diagnostic string) []byte {
	notice := FailOpenNoticeText(reason, diagnostic)
	if notice == "" {
		return ClaudeAllow()
	}
	encoded, err := json.Marshal(claudeFailOpenPayload{SystemMessage: notice})
	if err != nil {
		return ClaudeAllow()
	}
	return append(encoded, '\n')
}

// failOpenStderr renders the same warning for providers with no structured
// channel for it. Writing it to stderr on an allow keeps the event in the
// transcript rather than leaving the outage invisible.
func failOpenStderr(reason FailOpenReason, diagnostic string) []byte {
	notice := FailOpenNoticeText(reason, diagnostic)
	if notice == "" {
		return nil
	}
	return fmt.Appendf(nil, "agent-gate: %s\n", notice)
}
