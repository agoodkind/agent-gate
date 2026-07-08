package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
)

func TestLoadValidatesRuleDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "capture-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 1
action = "block"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].DiagnosticGroup != 1 {
		t.Fatalf("loaded diagnostic_group = %#v", cfg.Rules)
	}
}

func TestLoadDefaultsRuleDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "default-format"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %#v", cfg.Rules)
	}
	if cfg.Rules[0].DiagnosticFormat != config.DiagnosticFormatDetailed {
		t.Fatalf("DiagnosticFormat = %q, want %q", cfg.Rules[0].DiagnosticFormat, config.DiagnosticFormatDetailed)
	}
}

func TestLoadAcceptsMessageOnlyDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "message-only"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
diagnostic_format = "message_only"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %#v", cfg.Rules)
	}
	if cfg.Rules[0].DiagnosticFormat != config.DiagnosticFormatMessageOnly {
		t.Fatalf("DiagnosticFormat = %q, want %q", cfg.Rules[0].DiagnosticFormat, config.DiagnosticFormatMessageOnly)
	}
}

func TestLoadRejectsUnknownDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "bad-format"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
diagnostic_format = "compact"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for unknown diagnostic_format")
	}
	if !strings.Contains(err.Error(), `unknown diagnostic_format "compact"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInvalidDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "bad-group"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 2
action = "block"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for invalid diagnostic_group")
	}
	if !strings.Contains(err.Error(), "diagnostic_group 2 exceeds capture count 1") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadValidatesConditionDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "condition-capture-rule"
events = ["Stop"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 1
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Conditions[0].DiagnosticGroup != 1 {
		t.Fatalf("loaded condition diagnostic_group = %#v", cfg.Rules)
	}
}

func TestLoadExistingRequiresFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-gate", "config.toml")

	_, err := config.LoadExisting(path)
	if err == nil {
		t.Fatal("LoadExisting() returned nil error for missing config")
	}
	if !strings.Contains(err.Error(), "stat config") {
		t.Fatalf("LoadExisting() error = %v", err)
	}
}

func TestLoadTelemetryBlock(t *testing.T) {
	setConfigHome(t, `
[telemetry]
otlp_endpoint = "127.0.0.1:4317"
slow_op_threshold_ms = 50
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Telemetry.OTLPEndpoint != "127.0.0.1:4317" {
		t.Errorf("OTLPEndpoint = %q, want %q", cfg.Telemetry.OTLPEndpoint, "127.0.0.1:4317")
	}
	if cfg.Telemetry.SlowOpThresholdMs != 50 {
		t.Errorf("SlowOpThresholdMs = %d, want 50", cfg.Telemetry.SlowOpThresholdMs)
	}
}

func TestLoadJudgeDefaultsAndOverrides(t *testing.T) {
	setConfigHome(t, ``)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.JudgeEnabled() {
		t.Fatal("JudgeEnabled = true, want false by default")
	}
	if cfg.JudgeLMReviewGRPCAddress() != "" {
		t.Fatalf("JudgeLMReviewGRPCAddress = %q, want empty", cfg.JudgeLMReviewGRPCAddress())
	}
	if cfg.JudgeClydeGRPCAddress() != "" {
		t.Fatalf("JudgeClydeGRPCAddress = %q, want empty", cfg.JudgeClydeGRPCAddress())
	}
	if cfg.JudgeDisagreementLogPath() != config.DefaultJudgeDisagreementLogPath() {
		t.Fatalf("JudgeDisagreementLogPath = %q, want default", cfg.JudgeDisagreementLogPath())
	}

	setConfigHome(t, `
[judge]
enabled = true
lm_review_grpc_address = "127.0.0.1:5501"
clyde_grpc_address = "127.0.0.1:5502"
disagreement_log_path = "/tmp/agent-gate-disagreements.jsonl"
`)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() override error: %v", err)
	}
	if !cfg.JudgeEnabled() {
		t.Fatal("JudgeEnabled = false, want true")
	}
	if cfg.JudgeLMReviewGRPCAddress() != "127.0.0.1:5501" {
		t.Fatalf("JudgeLMReviewGRPCAddress = %q", cfg.JudgeLMReviewGRPCAddress())
	}
	if cfg.JudgeClydeGRPCAddress() != "127.0.0.1:5502" {
		t.Fatalf("JudgeClydeGRPCAddress = %q", cfg.JudgeClydeGRPCAddress())
	}
	if cfg.JudgeDisagreementLogPath() != "/tmp/agent-gate-disagreements.jsonl" {
		t.Fatalf("JudgeDisagreementLogPath = %q", cfg.JudgeDisagreementLogPath())
	}
}

func TestHookCachePerformanceDefaultsAndOverrides(t *testing.T) {
	setConfigHome(t, ``)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.HookCacheMaxEntries() != hotkv.DefaultMaxEntries {
		t.Fatalf("HookCacheMaxEntries = %d, want %d", cfg.HookCacheMaxEntries(), hotkv.DefaultMaxEntries)
	}
	if cfg.HookCacheMaxValueBytes() != hotkv.DefaultMaxValueBytes {
		t.Fatalf("HookCacheMaxValueBytes = %d, want %d", cfg.HookCacheMaxValueBytes(), hotkv.DefaultMaxValueBytes)
	}
	if cfg.HookCachePruneInterval() != hotkv.DefaultPruneInterval {
		t.Fatalf("HookCachePruneInterval = %s, want %s", cfg.HookCachePruneInterval(), hotkv.DefaultPruneInterval)
	}

	setConfigHome(t, `
[performance.hook.cache]
max_entries = 12
max_value_bytes = 34
prune_interval_ms = 56
`)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() override error: %v", err)
	}
	if cfg.HookCacheMaxEntries() != 12 {
		t.Fatalf("HookCacheMaxEntries override = %d, want 12", cfg.HookCacheMaxEntries())
	}
	if cfg.HookCacheMaxValueBytes() != 34 {
		t.Fatalf("HookCacheMaxValueBytes override = %d, want 34", cfg.HookCacheMaxValueBytes())
	}
	if cfg.HookCachePruneInterval() != 56*time.Millisecond {
		t.Fatalf("HookCachePruneInterval override = %s, want 56ms", cfg.HookCachePruneInterval())
	}
}

func TestLoadActionAuditSetsAuditOnly(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "audit-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "audit"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Action != config.ActionAudit {
		t.Fatalf("rule action = %q, want %q", cfg.Rules[0].Action, config.ActionAudit)
	}
	if !cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = false, want true")
	}
}

func TestLoadActionDefaultsToBlock(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "default-action-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Action != config.ActionBlock {
		t.Fatalf("rule action = %q, want %q", cfg.Rules[0].Action, config.ActionBlock)
	}
	if cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = true, want false for default action")
	}
}

func TestLoadRejectsUnknownAction(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "unknown-action-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "warn"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for unknown action")
	}
	if !strings.Contains(err.Error(), `unknown action "warn"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.toml.example")

	cfg, err := config.LoadExisting(examplePath)
	if err != nil {
		t.Fatalf("LoadExisting(%q) error: %v", examplePath, err)
	}
	if len(cfg.Rules) == 0 {
		t.Fatal("example config loaded zero rules")
	}
}

func TestEnsureDefaultsCreatesCanonicalConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("EnsureDefaults() error: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, `[update]`) {
		t.Fatalf("config missing [update] block:\n%s", got)
	}
	if !strings.Contains(got, `mode = "apply"`) {
		t.Fatalf("config missing apply mode:\n%s", got)
	}
	if !strings.Contains(got, "allow_prerelease = true") {
		t.Fatalf("config missing rolling channel default:\n%s", got)
	}
}

func TestEnsureDefaultsAppendsMissingUpdateTable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	path := filepath.Join(configDir, "config.toml")
	initial := "[log]\nlevel = \"debug\"\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("EnsureDefaults() error: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, initial) {
		t.Fatalf("config lost existing content:\n%s", got)
	}
	if strings.Count(got, "[update]") != 1 {
		t.Fatalf("config expected one [update] block:\n%s", got)
	}
	if !strings.Contains(got, "allow_prerelease = true") {
		t.Fatalf("config missing rolling channel default:\n%s", got)
	}
}

func TestEnsureDefaultsOverridesExistingUpdateModeWhenRequested(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	path := filepath.Join(configDir, "config.toml")
	initial := "[update]\nenabled = true\nmode = \"check\"\ninterval = \"24h\"\nrepo = \"agoodkind/agent-gate\"\nallow_prerelease = false\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{AutoUpdateMode: "off"})
	if err != nil {
		t.Fatalf("EnsureDefaults() error: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)
	if !strings.Contains(got, "enabled = false") {
		t.Fatalf("config did not disable updater:\n%s", got)
	}
	if !strings.Contains(got, `mode = "apply"`) {
		t.Fatalf("config did not rewrite mode for off override:\n%s", got)
	}
}

func TestEnsureDefaultsPreservesExistingUpdateSettingsWhenOverridingMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	path := filepath.Join(configDir, "config.toml")
	initial := "[update]\nenabled = false\nmode = \"check\"\ninterval = \"48h\"\nrepo = \"example/custom\"\nallow_prerelease = true\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{AutoUpdateMode: "apply"})
	if err != nil {
		t.Fatalf("EnsureDefaults() error: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)
	for _, want := range []string{
		"enabled = true",
		`mode = "apply"`,
		`interval = "48h"`,
		`repo = "example/custom"`,
		"allow_prerelease = true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
}

func TestEnsureDefaultsPreservesExplicitStableChannel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	path := filepath.Join(configDir, "config.toml")
	initial := "[update]\nenabled = true\nmode = \"check\"\ninterval = \"24h\"\nrepo = \"agoodkind/agent-gate\"\nallow_prerelease = false\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{AutoUpdateMode: "apply"})
	if err != nil {
		t.Fatalf("EnsureDefaults() error: %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(content)
	for _, want := range []string{
		"enabled = true",
		`mode = "apply"`,
		"allow_prerelease = false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
}

func TestLoadRejectsWhitespaceOnlyStdoutJSONField(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
stdout_json_field = "   "
stdout_json_equals = true
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for whitespace-only stdout_json_field")
	}
	if !strings.Contains(err.Error(), "stdout_json_field and stdout_json_equals must be set together") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsStdoutJSONFieldWithEmptySegment(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
stdout_json_field = "a..b"
stdout_json_equals = true
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for stdout_json_field with empty segment")
	}
	if !strings.Contains(err.Error(), "stdout_json_field: must not contain empty path segments") {
		t.Fatalf("Load() error = %v", err)
	}
}

func setConfigHome(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}
