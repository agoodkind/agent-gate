package composer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDecideUsesOracleBlockAndLogsLLMDisagreement(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Block, "indexed root"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{Verdict: Allow, Reason: "model allow"}, nil
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("search-guard", "grep -rn TODO .", "/repo")
	if got != Block {
		t.Fatalf("Decide = %v, want block", got)
	}
	records := readDisagreementRecords(t, logPath)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].OracleVerdict != "block" || records[0].LLMVerdict != "allow" {
		t.Fatalf("record verdicts = %#v", records[0])
	}
}

func TestDecideSkipsLogWhenOracleAndLLMAgree(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Allow, "outside roots"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{Verdict: Allow, Reason: "model allow"}, nil
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("search-guard", "grep foo /etc/hosts", "/repo")
	if got != Allow {
		t.Fatalf("Decide = %v, want allow", got)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log path exists or stat failed: %v", err)
	}
}

func TestDecideUsesLLMWhenOracleUnknownAndLogs(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Unknown, "dynamic command"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{Verdict: Block, Reason: "model block"}, nil
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("search-guard", `sh -c "grep TODO ."`, "/repo")
	if got != Block {
		t.Fatalf("Decide = %v, want block", got)
	}
	records := readDisagreementRecords(t, logPath)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Reason != "oracle_unknown" {
		t.Fatalf("reason = %q, want oracle_unknown", records[0].Reason)
	}
}

func TestDecideFailsClosedWhenOracleUnknownAndLLMErrors(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Unknown, "dynamic command"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{}, errors.New("judge unavailable")
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("search-guard", `eval "$cmd"`, "/repo")
	if got != Block {
		t.Fatalf("Decide = %v, want fail-closed block", got)
	}
	records := readDisagreementRecords(t, logPath)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].LLMError == "" {
		t.Fatalf("LLMError = empty, want error")
	}
}

func TestDecideUnionBlocksWhenEitherBlocks(t *testing.T) {
	// Default authority is union: the model's block is honored even though the
	// oracle allowed (the oracle could not see the laundering).
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Allow, "oracle saw nothing to block"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{Verdict: Block, Reason: "model caught it"}, nil
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("worktree-guard", "some laundered write", "/repo")
	if got != Block {
		t.Fatalf("Decide = %v, want block (union superset)", got)
	}
	records := readDisagreementRecords(t, logPath)
	if len(records) != 1 || records[0].EnforcedVerdict != "block" || records[0].OracleVerdict != "allow" {
		t.Fatalf("record = %#v, want enforced block over oracle allow", records)
	}
}

func TestDecideUnionFailsClosedWhenNeitherDecides(t *testing.T) {
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Unknown, "dynamic"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{}, errors.New("judge unavailable")
		},
	})
	if got := runtime.Decide("worktree-guard", `eval "$x"`, "/repo"); got != Block {
		t.Fatalf("Decide = %v, want fail-closed block", got)
	}
}

func TestDecideLLMAuthoritativeEnforcesModelOverOracle(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "disagreements.jsonl")
	runtime := NewRuntime(RuntimeOptions{
		Authority: AuthorityLLM,
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Block, "oracle would block"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{Verdict: Allow, Reason: "model allow"}, nil
		},
		DisagreementLogPath: logPath,
	})

	got := runtime.Decide("search-guard", "grep -rn TODO .", "/repo")
	if got != Allow {
		t.Fatalf("Decide = %v, want allow (model authoritative)", got)
	}
	records := readDisagreementRecords(t, logPath)
	if len(records) != 1 || records[0].EnforcedVerdict != "allow" || records[0].OracleVerdict != "block" {
		t.Fatalf("record = %#v, want enforced allow over oracle block", records)
	}
}

func TestDecideLLMAuthoritativeFallsBackToOracleOnModelError(t *testing.T) {
	runtime := NewRuntime(RuntimeOptions{
		Authority: AuthorityLLM,
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			return Block, "oracle block"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			return JudgeResult{}, errors.New("judge unavailable")
		},
	})

	got := runtime.Decide("search-guard", "grep -rn TODO .", "/repo")
	if got != Block {
		t.Fatalf("Decide = %v, want oracle-block fallback when model errors", got)
	}
}

func TestDecideRunsOracleAndLLMConcurrently(t *testing.T) {
	runtime := NewRuntime(RuntimeOptions{
		Oracle: func(string, string, string, Deps) (Verdict, string) {
			time.Sleep(150 * time.Millisecond)
			return Allow, "outside roots"
		},
		Judge: func(string, string, string, Deps, []string) (JudgeResult, error) {
			time.Sleep(150 * time.Millisecond)
			return JudgeResult{Verdict: Allow, Reason: "model allow"}, nil
		},
	})

	started := time.Now()
	got := runtime.Decide("search-guard", "grep foo /etc/hosts", "/repo")
	elapsed := time.Since(started)
	if got != Allow {
		t.Fatalf("Decide = %v, want allow", got)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("elapsed = %s, want roughly one 150ms sleep", elapsed)
	}
}

func readDisagreementRecords(t *testing.T, path string) []DisagreementRecord {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	lines := splitJSONLines(string(content))
	records := make([]DisagreementRecord, 0, len(lines))
	for _, line := range lines {
		var record DisagreementRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("Unmarshal(%q): %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func splitJSONLines(content string) []string {
	var lines []string
	for _, line := range stringsSplitLines(content) {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func stringsSplitLines(content string) []string {
	var lines []string
	start := 0
	for index, char := range content {
		if char != '\n' {
			continue
		}
		lines = append(lines, content[start:index])
		start = index + 1
	}
	if start < len(content) {
		lines = append(lines, content[start:])
	}
	return lines
}
