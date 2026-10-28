package hook_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

type capabilityLogRecord struct {
	Level         string `json:"level"`
	Message       string `json:"msg"`
	Rules         int    `json:"rules"`
	Subscriptions int    `json:"subscriptions"`
	Rule          string `json:"rule"`
	Provider      string `json:"provider"`
	Event         string `json:"event"`
	Effect        string `json:"effect"`
}

func TestCapabilityWarningsSummarizeUnsupportedSubscriptions(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{
		{Name: "observe", Action: config.ActionBlock, ClaudeEvents: []string{"PostToolUse", "PostToolUseFailure"}},
		{Name: "context", Action: config.ActionInject, CursorEvents: []string{"beforeSubmitPrompt"}, Output: "context"},
		{Name: "supported", Action: config.ActionBlock, ClaudeEvents: []string{"PreToolUse"}},
	}}
	for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
		t.Run(level.String(), func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: level}))
			downgrades := hook.WarnCapabilityDowngrades(t.Context(), logger, cfg)
			if len(downgrades) != 3 {
				t.Fatalf("unsupported subscriptions = %d, want 3", len(downgrades))
			}
			decoder := json.NewDecoder(&output)
			warningCount, detailCount := 0, 0
			for {
				var record capabilityLogRecord
				err := decoder.Decode(&record)
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				switch record.Level {
				case "WARN":
					warningCount++
					if record.Rules != 2 || record.Subscriptions != 3 {
						t.Fatalf("warning summary = %+v", record)
					}
				case "DEBUG":
					detailCount++
					if record.Rule == "" || record.Provider == "" || record.Event == "" || record.Effect == "" {
						t.Fatalf("incomplete debug detail = %+v", record)
					}
				default:
					t.Fatalf("unexpected diagnostic level: %+v", record)
				}
			}
			if warningCount != 1 {
				t.Fatalf("warning records = %d, want one summary", warningCount)
			}
			if level == slog.LevelDebug && detailCount != 3 {
				t.Fatalf("debug details = %d, want 3", detailCount)
			}
			if level == slog.LevelInfo && detailCount != 0 {
				t.Fatalf("ordinary logging included %d debug details", detailCount)
			}
		})
	}
}

func TestCapabilityWarningsSkipSupportedSubscriptions(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	cfg := &config.Config{Rules: []config.Rule{{
		Name: "supported", Action: config.ActionBlock, ClaudeEvents: []string{"PreToolUse"},
	}}}
	if downgrades := hook.WarnCapabilityDowngrades(t.Context(), logger, cfg); len(downgrades) != 0 {
		t.Fatalf("supported action downgraded: %+v", downgrades)
	}
	if output.Len() != 0 {
		t.Fatalf("supported action produced diagnostics: %s", output.String())
	}
}
