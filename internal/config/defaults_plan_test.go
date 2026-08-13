package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/agent-gate/internal/config"
)

func TestApplyDefaultsReplacesConfigurationAtomically(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := []byte("[log]\nlevel = \"debug\"\n")
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AutoUpdateMode: config.UpdateModeCheck,
		AuditProfile:   config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if got, err := os.ReadFile(configPath); err != nil || string(got) != string(initial) {
		t.Fatalf("PrepareDefaults changed config: content=%q err=%v", got, err)
	}
	if err := os.WriteFile(configPath, []byte("changed after prepare\n"), 0o600); err != nil {
		t.Fatalf("WriteFile intervening config: %v", err)
	}

	path, err := config.ApplyDefaults(plan)
	if err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if path != configPath {
		t.Fatalf("ApplyDefaults path = %q, want %q", path, configPath)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile applied config: %v", err)
	}
	if string(content) != string(plan.Content) {
		t.Fatalf("applied content differs from prepared bytes\nwant:\n%s\ngot:\n%s", plan.Content, content)
	}
	for _, want := range []string{`profile = "minimal"`, `mode = "check"`} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("applied config missing %q:\n%s", want, content)
		}
	}
	entries, err := os.ReadDir(configDir)
	if err != nil {
		t.Fatalf("ReadDir config directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		t.Fatalf("config directory entries = %v, want only config.toml", entries)
	}
}

func TestApplyDefaultsPreservesExistingConfigurationMode(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o400); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if _, err := config.ApplyDefaults(plan); err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Stat config: %v", err)
	}
	if info.Mode().Perm() != 0o400 {
		t.Fatalf("config mode = %o, want 400", info.Mode().Perm())
	}
}

func TestApplyDefaultsClosesPreparedIdentityHandles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	baseline := openFileDescriptorCount(t)
	plans := make([]*config.DefaultsPlan, 0, 20)
	for range 20 {
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		if _, err := config.ApplyDefaults(plan); err != nil {
			t.Fatalf("ApplyDefaults: %v", err)
		}
		plans = append(plans, plan)
	}
	if got := openFileDescriptorCount(t); got > baseline+4 {
		t.Fatalf("open file descriptors = %d, baseline = %d", got, baseline)
	}
	if _, err := config.ApplyDefaults(plans[0]); err == nil ||
		!strings.Contains(err.Error(), "already been consumed") {
		t.Fatalf("second ApplyDefaults error = %v, want consumed plan", err)
	}
}

func TestApplyDefaultsClosesPreparedIdentityHandlesAfterFailure(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	baseline := openFileDescriptorCount(t)
	plans := make([]*config.DefaultsPlan, 0, 20)
	for range 20 {
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		if err := os.Remove(configPath); err != nil {
			t.Fatalf("Remove config: %v", err)
		}
		if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"info\"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile replacement: %v", err)
		}
		if _, err := config.ApplyDefaults(plan); err == nil {
			t.Fatal("ApplyDefaults succeeded after target replacement")
		}
		plans = append(plans, plan)
	}
	if got := openFileDescriptorCount(t); got > baseline+4 {
		t.Fatalf("open file descriptors = %d, baseline = %d", got, baseline)
	}
}

func TestDefaultsPlanCloseReleasesPreparedIdentityHandles(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	baseline := openFileDescriptorCount(t)
	plans := make([]*config.DefaultsPlan, 0, 20)
	for range 20 {
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		if err := plan.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		plans = append(plans, plan)
	}
	if got := openFileDescriptorCount(t); got > baseline+4 {
		t.Fatalf("open file descriptors = %d, baseline = %d", got, baseline)
	}
	if _, err := config.ApplyDefaults(plans[0]); err == nil {
		t.Fatal("ApplyDefaults succeeded after Close")
	}
}

func openFileDescriptorCount(t *testing.T) int {
	t.Helper()
	count := 0
	for descriptor := range 4096 {
		if _, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0); err == nil {
			count++
		} else if !errors.Is(err, unix.EBADF) {
			t.Fatalf("inspect file descriptor %d: %v", descriptor, err)
		}
	}
	return count
}

func TestPrepareDefaultsPreservesAuditStorageOverrides(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := `[audit.storage]
profile = "full"
maintenance_batch_rows = 17
full_detail_retention = "48h"
summary_retention = "96h"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	for _, want := range []string{
		`profile = "minimal"`,
		"maintenance_batch_rows = 17",
		`full_detail_retention = "48h"`,
		`summary_retention = "96h"`,
	} {
		if !strings.Contains(string(plan.Content), want) {
			t.Fatalf("prepared config missing %q:\n%s", want, plan.Content)
		}
	}
}

func TestPrepareDefaultsPreservesProfileTrailingComment(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := "[audit.storage]\nprofile = \"full\"  # keep this rationale\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	want := "profile = \"minimal\"  # keep this rationale\n"
	if !strings.Contains(string(plan.Content), want) {
		t.Fatalf("prepared config missing %q:\n%s", want, plan.Content)
	}
}

func TestPrepareDefaultsIgnoresAuditStorageTextInsideMultilineString(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := `[audit.outputs.sqlite]
path = """
[audit.storage]
profile = "full"
"""
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if !strings.Contains(string(plan.Content), initial) {
		t.Fatalf("prepared config changed multiline string:\n%s", plan.Content)
	}
	if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
		t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
	}
}

func TestPrepareDefaultsReplacesMultilineAuditStorageProfile(t *testing.T) {
	testCases := []struct {
		name      string
		delimiter string
	}{
		{name: "basic", delimiter: `"""`},
		{name: "literal", delimiter: `'''`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			configDir := filepath.Join(configHome, "agent-gate")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("MkdirAll config directory: %v", err)
			}
			configPath := filepath.Join(configDir, "config.toml")
			initial := "[audit.storage]\nprofile = " + testCase.delimiter +
				"\nfull\n" + testCase.delimiter + "  # selected profile\n"
			if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
				t.Fatalf("WriteFile initial config: %v", err)
			}

			plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
				AuditProfile: config.AuditStorageProfileMinimal,
			})
			if err != nil {
				t.Fatalf("PrepareDefaults: %v", err)
			}
			if !strings.Contains(
				string(plan.Content),
				"profile = \"minimal\"  # selected profile\n",
			) {
				t.Fatalf("prepared config lost multiline profile suffix:\n%s", plan.Content)
			}
			if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
				t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
			}
		})
	}
}

func TestPrepareDefaultsAddsBalancedStorageOnlyWhenAbsent(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := "[log]\nlevel = \"info\"\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if !strings.Contains(string(plan.Content), "[audit.storage]\nprofile = \"balanced\"") {
		t.Fatalf("prepared config missing balanced storage:\n%s", plan.Content)
	}

	initial = "[audit.storage]\nprofile = \"full\"\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile existing storage config: %v", err)
	}
	plan, err = config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults existing storage: %v", err)
	}
	if !strings.Contains(string(plan.Content), `profile = "full"`) {
		t.Fatalf("prepared config replaced existing profile:\n%s", plan.Content)
	}
}

func TestPrepareDefaultsPreservesImplicitStorageParent(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := "[audit.storage.detail]\nwire_input = false\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile initial config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if strings.Contains(string(plan.Content), "[audit.storage]\n") {
		t.Fatalf("prepared config inserted explicit parent:\n%s", plan.Content)
	}
	if !strings.Contains(string(plan.Content), "wire_input = false") {
		t.Fatalf("prepared config lost storage override:\n%s", plan.Content)
	}

	plan, err = config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults profile override: %v", err)
	}
	parentIndex := strings.Index(string(plan.Content), "[audit.storage]\n")
	detailIndex := strings.Index(string(plan.Content), "[audit.storage.detail]\n")
	if parentIndex < 0 || detailIndex < 0 || parentIndex >= detailIndex {
		t.Fatalf("prepared config did not define parent before detail:\n%s", plan.Content)
	}
}

func TestPrepareDefaultsHandlesQuotedAuditStorageKeys(t *testing.T) {
	testCases := []struct {
		name    string
		initial string
		want    string
	}{
		{
			name:    "quoted profile",
			initial: "[audit.storage]\n\"profile\" = \"full\"\n",
			want:    "[audit.storage]\n\"profile\" = \"minimal\"\n",
		},
		{
			name:    "quoted dotted header",
			initial: "[\"audit\".\"storage\"]\nprofile = \"full\"\n",
			want:    "[\"audit\".\"storage\"]\nprofile = \"minimal\"\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			configDir := filepath.Join(configHome, "agent-gate")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("MkdirAll config directory: %v", err)
			}
			if err := os.WriteFile(
				filepath.Join(configDir, "config.toml"),
				[]byte(testCase.initial),
				0o600,
			); err != nil {
				t.Fatalf("WriteFile config: %v", err)
			}

			plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
				AuditProfile: config.AuditStorageProfileMinimal,
			})
			if err != nil {
				t.Fatalf("PrepareDefaults: %v", err)
			}
			if !strings.Contains(string(plan.Content), testCase.want) {
				t.Fatalf("prepared content missing %q:\n%s", testCase.want, plan.Content)
			}
			if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
				t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
			}
		})
	}
}

func TestPrepareDefaultsPreservesLiteralDottedTable(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := "[\"audit.storage\"]\nprofile = \"unrelated\"\n"
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if !strings.Contains(string(plan.Content), initial) {
		t.Fatalf("prepared config changed literal dotted table:\n%s", plan.Content)
	}
	if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
		t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
	}
}

func TestEnsureDefaultsPreservesConfigSymlinkCompatibility(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	targetPath := filepath.Join(t.TempDir(), "managed-config.toml")
	if err := os.WriteFile(targetPath, []byte("[log]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile target config: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.Symlink(targetPath, configPath); err != nil {
		t.Fatalf("Symlink config: %v", err)
	}

	if _, err := config.EnsureDefaults(config.EnsureDefaultsOptions{}); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		t.Fatalf("Lstat config: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config mode = %v, want symlink", info.Mode())
	}
	targetContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile target config: %v", err)
	}
	if !strings.Contains(string(targetContent), "[audit.storage]") {
		t.Fatalf("target config was not updated:\n%s", targetContent)
	}
}

func TestApplyDefaultsRejectsRetargetedConfigSymlink(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	originalTarget := filepath.Join(t.TempDir(), "original.toml")
	replacementTarget := filepath.Join(t.TempDir(), "replacement.toml")
	originalContent := []byte("[log]\nlevel = \"debug\"\n")
	replacementContent := []byte("[log]\nlevel = \"warn\"\n")
	if err := os.WriteFile(originalTarget, originalContent, 0o600); err != nil {
		t.Fatalf("WriteFile original target: %v", err)
	}
	if err := os.WriteFile(replacementTarget, replacementContent, 0o600); err != nil {
		t.Fatalf("WriteFile replacement target: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.Symlink(originalTarget, configPath); err != nil {
		t.Fatalf("Symlink original config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("Remove original symlink: %v", err)
	}
	if err := os.Symlink(replacementTarget, configPath); err != nil {
		t.Fatalf("Symlink replacement config: %v", err)
	}

	if _, err := config.ApplyDefaults(plan); err == nil {
		t.Fatal("ApplyDefaults returned nil error for retargeted config symlink")
	}
	assertConfigBytes(t, originalTarget, originalContent)
	assertConfigBytes(t, replacementTarget, replacementContent)
}

func TestApplyDefaultsRejectsChangedConfigPathIdentity(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(t *testing.T, configPath string, targetPath string)
		check  func(t *testing.T, targetPath string, originalContent []byte)
	}{
		{
			name: "recreated config symlink",
			mutate: func(t *testing.T, configPath string, targetPath string) {
				t.Helper()
				if err := os.Remove(configPath); err != nil {
					t.Fatalf("Remove config symlink: %v", err)
				}
				if err := os.Symlink(targetPath, configPath); err != nil {
					t.Fatalf("Recreate config symlink: %v", err)
				}
			},
			check: assertUnchangedConfigTarget,
		},
		{
			name: "removed target",
			mutate: func(t *testing.T, _ string, targetPath string) {
				t.Helper()
				if err := os.Remove(targetPath); err != nil {
					t.Fatalf("Remove target: %v", err)
				}
			},
			check: func(t *testing.T, targetPath string, _ []byte) {
				t.Helper()
				if _, err := os.Lstat(targetPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Lstat removed target error = %v, want not found", err)
				}
			},
		},
		{
			name: "replaced target",
			mutate: func(t *testing.T, _ string, targetPath string) {
				t.Helper()
				if err := os.Remove(targetPath); err != nil {
					t.Fatalf("Remove target: %v", err)
				}
				if err := os.WriteFile(targetPath, []byte("replacement target\n"), 0o600); err != nil {
					t.Fatalf("WriteFile replacement target: %v", err)
				}
			},
			check: func(t *testing.T, targetPath string, _ []byte) {
				t.Helper()
				assertConfigBytes(t, targetPath, []byte("replacement target\n"))
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			configDir := filepath.Join(configHome, "agent-gate")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("MkdirAll config directory: %v", err)
			}
			targetPath := filepath.Join(t.TempDir(), "target.toml")
			originalContent := []byte("[log]\nlevel = \"debug\"\n")
			if err := os.WriteFile(targetPath, originalContent, 0o600); err != nil {
				t.Fatalf("WriteFile target: %v", err)
			}
			configPath := filepath.Join(configDir, "config.toml")
			if err := os.Symlink(targetPath, configPath); err != nil {
				t.Fatalf("Symlink config: %v", err)
			}
			plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
			if err != nil {
				t.Fatalf("PrepareDefaults: %v", err)
			}

			testCase.mutate(t, configPath, targetPath)
			if _, err := config.ApplyDefaults(plan); err == nil {
				t.Fatal("ApplyDefaults returned nil error for changed path identity")
			}
			testCase.check(t, targetPath, originalContent)
		})
	}
}

func TestApplyDefaultsRejectsRedirectedMissingParent(t *testing.T) {
	t.Run("created config parent", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		configDir := filepath.Join(configHome, "agent-gate")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatalf("MkdirAll config directory: %v", err)
		}

		if _, err := config.ApplyDefaults(plan); err == nil {
			t.Fatal("ApplyDefaults returned nil error for externally created config parent")
		}
		assertConfigPathMissing(t, filepath.Join(configDir, "config.toml"))
	})

	t.Run("missing config parent", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		unexpectedDir := t.TempDir()
		configDir := filepath.Join(configHome, "agent-gate")
		if err := os.Symlink(unexpectedDir, configDir); err != nil {
			t.Fatalf("Symlink config directory: %v", err)
		}

		if _, err := config.ApplyDefaults(plan); err == nil {
			t.Fatal("ApplyDefaults returned nil error for redirected config parent")
		}
		assertConfigPathMissing(t, filepath.Join(unexpectedDir, "config.toml"))
	})

	t.Run("broken symlink target parent", func(t *testing.T) {
		configHome := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configHome)
		configDir := filepath.Join(configHome, "agent-gate")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatalf("MkdirAll config directory: %v", err)
		}
		targetParent := filepath.Join(t.TempDir(), "managed")
		if err := os.MkdirAll(targetParent, 0o700); err != nil {
			t.Fatalf("MkdirAll target parent: %v", err)
		}
		targetPath := filepath.Join(targetParent, "config.toml")
		configPath := filepath.Join(configDir, "config.toml")
		if err := os.Symlink(targetPath, configPath); err != nil {
			t.Fatalf("Symlink config: %v", err)
		}
		plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
		if err != nil {
			t.Fatalf("PrepareDefaults: %v", err)
		}
		if err := os.Remove(targetParent); err != nil {
			t.Fatalf("Remove target parent: %v", err)
		}
		unexpectedDir := t.TempDir()
		if err := os.Symlink(unexpectedDir, targetParent); err != nil {
			t.Fatalf("Symlink target parent: %v", err)
		}

		if _, err := config.ApplyDefaults(plan); err == nil {
			t.Fatal("ApplyDefaults returned nil error for redirected target parent")
		}
		assertConfigPathMissing(t, filepath.Join(unexpectedDir, "config.toml"))
	})
}

func TestPrepareDefaultsHandlesDottedAuditStorageAssignments(t *testing.T) {
	testCases := []struct {
		name    string
		initial string
		want    string
	}{
		{
			name:    "root dotted key",
			initial: "audit.storage.profile = \"full\"\n",
			want:    "audit.storage.profile = \"minimal\"\n",
		},
		{
			name:    "audit table relative dotted key",
			initial: "[audit]\nstorage.profile = \"full\"\n",
			want:    "[audit]\nstorage.profile = \"minimal\"\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			configHome := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configHome)
			configDir := filepath.Join(configHome, "agent-gate")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatalf("MkdirAll config directory: %v", err)
			}
			if err := os.WriteFile(
				filepath.Join(configDir, "config.toml"),
				[]byte(testCase.initial),
				0o600,
			); err != nil {
				t.Fatalf("WriteFile config: %v", err)
			}

			plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
				AuditProfile: config.AuditStorageProfileMinimal,
			})
			if err != nil {
				t.Fatalf("PrepareDefaults: %v", err)
			}
			if !strings.Contains(string(plan.Content), testCase.want) {
				t.Fatalf("prepared config missing %q:\n%s", testCase.want, plan.Content)
			}
			if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
				t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
			}
		})
	}
}

func TestEnsureDefaultsCreatesBrokenRelativeSymlinkTarget(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	targetDir := filepath.Join(configHome, "managed")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatalf("MkdirAll target directory: %v", err)
	}
	targetPath := filepath.Join(targetDir, "config.toml")
	configPath := filepath.Join(configDir, "config.toml")
	relativeTarget, err := filepath.Rel(configDir, targetPath)
	if err != nil {
		t.Fatalf("Rel target: %v", err)
	}
	if err := os.Symlink(relativeTarget, configPath); err != nil {
		t.Fatalf("Symlink config: %v", err)
	}

	if _, err := config.EnsureDefaults(config.EnsureDefaultsOptions{}); err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	info, err := os.Lstat(configPath)
	if err != nil {
		t.Fatalf("Lstat config: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config mode = %v, want symlink", info.Mode())
	}
	if _, err := config.LoadExisting(targetPath); err != nil {
		t.Fatalf("LoadExisting created target: %v", err)
	}
}

func TestPrepareDefaultsReplacesInlineAuditStorageProfile(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	initial := `audit.storage = { profile = "full", maintenance_batch_rows = 17 }
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
		AuditProfile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	if plan.Config.AuditStoragePolicy().Profile != config.AuditStorageProfileMinimal {
		t.Fatalf("profile = %q, want minimal", plan.Config.AuditStoragePolicy().Profile)
	}
	if plan.Config.AuditStoragePolicy().MaintenanceBatchRows != 17 {
		t.Fatalf("maintenance batch rows = %d, want 17", plan.Config.AuditStoragePolicy().MaintenanceBatchRows)
	}
}

func TestApplyDefaultsUsesImmutablePreparedBytes(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{})
	if err != nil {
		t.Fatalf("PrepareDefaults: %v", err)
	}
	plan.Content = []byte("invalid substituted bytes")
	plan.Path = filepath.Join(t.TempDir(), "wrong.toml")
	plan.Config = nil

	path, err := config.ApplyDefaults(plan)
	if err != nil {
		t.Fatalf("ApplyDefaults: %v", err)
	}
	if path != filepath.Join(configHome, "agent-gate", "config.toml") {
		t.Fatalf("applied path = %q", path)
	}
	if _, err := config.LoadExisting(path); err != nil {
		t.Fatalf("LoadExisting applied config: %v", err)
	}
}

func assertConfigBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func assertUnchangedConfigTarget(t *testing.T, targetPath string, originalContent []byte) {
	t.Helper()
	assertConfigBytes(t, targetPath, originalContent)
}

func assertConfigPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat %s error = %v, want not found", path, err)
	}
}
