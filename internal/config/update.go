package config

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const (
	// UpdateModeApply installs verified release assets automatically.
	UpdateModeApply = "apply"
	// UpdateModeCheck records availability without installing.
	UpdateModeCheck = "check"
	// DefaultUpdateRepo is the direct-install release repository.
	DefaultUpdateRepo = "agoodkind/agent-gate"
)

//go:embed default_config.toml
var defaultConfigTOML string

// Update holds daemon-owned auto-update settings.
type Update struct {
	Enabled         *bool  `toml:"enabled"`
	Mode            string `toml:"mode"`
	Interval        string `toml:"interval"`
	Repo            string `toml:"repo"`
	AllowPrerelease *bool  `toml:"allow_prerelease"`
}

// EnsureDefaultsOptions controls install-time config creation and merging.
type EnsureDefaultsOptions struct {
	AutoUpdateMode string
}

// UpdateEnabled reports whether the daemon-owned updater should run.
func (c *Config) UpdateEnabled() bool {
	if c != nil && c.Update.Enabled != nil {
		return *c.Update.Enabled
	}
	return true
}

// UpdateMode returns the resolved update mode.
func (c *Config) UpdateMode() string {
	if c != nil && c.Update.Mode != "" {
		return c.Update.Mode
	}
	return UpdateModeApply
}

// UpdateRepo returns the release repository used for direct binary updates.
func (c *Config) UpdateRepo() string {
	if c != nil && c.Update.Repo != "" {
		return c.Update.Repo
	}
	return DefaultUpdateRepo
}

// UpdateInterval returns the resolved auto-update interval.
func (c *Config) UpdateInterval() time.Duration {
	if c != nil && c.Update.Interval != "" {
		interval, err := time.ParseDuration(c.Update.Interval)
		if err == nil && interval > 0 {
			return interval
		}
	}
	return defaultUpdateInterval
}

// UpdateAllowPrerelease reports whether update checks should include rolling releases.
func (c *Config) UpdateAllowPrerelease() bool {
	if c != nil && c.Update.AllowPrerelease != nil {
		return *c.Update.AllowPrerelease
	}
	return true
}

func normalizeUpdate(update *Update) error {
	log := slog.Default()
	if update == nil {
		return nil
	}
	if update.Mode != "" && update.Mode != UpdateModeApply && update.Mode != UpdateModeCheck {
		err := fmt.Errorf("update.mode: expected %q or %q, got %q", UpdateModeCheck, UpdateModeApply, update.Mode)
		log.Warn("config update normalize failed", "err", err)
		return err
	}
	if update.Interval != "" {
		interval, err := time.ParseDuration(update.Interval)
		if err != nil {
			log.Warn("config update interval parse failed", "interval", update.Interval, "err", err)
			return fmt.Errorf("update.interval %q: %w", update.Interval, err)
		}
		if interval <= 0 {
			err := fmt.Errorf("update.interval must be positive")
			log.Warn("config update interval rejected", "interval", update.Interval, "err", err)
			return err
		}
	}
	return nil
}

// EnsureDefaults creates or merges the canonical default config.
func EnsureDefaults(options EnsureDefaultsOptions) (string, error) {
	log := slog.Default()
	configPath := filepath.Clean(Path())
	log.Info("config ensure defaults", "path", configPath, "auto_update", options.AutoUpdateMode)
	if options.AutoUpdateMode != "" && options.AutoUpdateMode != UpdateModeApply && options.AutoUpdateMode != UpdateModeCheck && options.AutoUpdateMode != "off" {
		err := fmt.Errorf("auto-update mode must be %q, %q, or %q", UpdateModeCheck, UpdateModeApply, "off")
		log.Warn("config ensure defaults rejected mode", "path", configPath, "err", err)
		return configPath, err
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		log.Warn("config ensure defaults create dir failed", "path", configPath, "err", err)
		return configPath, fmt.Errorf("create config dir: %w", err)
	}
	current, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("config ensure defaults read failed", "path", configPath, "err", err)
			return configPath, fmt.Errorf("read config: %w", err)
		}
		content := canonicalConfigWithUpdateOverride(options.AutoUpdateMode)
		// #nosec G703 -- installer writes the canonical agent-gate config to the resolved XDG path.
		if writeErr := os.WriteFile(configPath, []byte(content), 0o600); writeErr != nil {
			log.Warn("config ensure defaults write failed", "path", configPath, "err", writeErr)
			return configPath, fmt.Errorf("write default config: %w", writeErr)
		}
		_, loadErr := LoadExisting(configPath)
		if loadErr != nil {
			log.Warn("config ensure defaults validation failed", "path", configPath, "err", loadErr)
		}
		return configPath, loadErr
	}

	next, changed, mergeErr := mergeUpdateDefaults(string(current), options.AutoUpdateMode)
	if mergeErr != nil {
		log.Warn("config ensure defaults merge failed", "path", configPath, "err", mergeErr)
		return configPath, mergeErr
	}
	if !changed {
		_, loadErr := LoadExisting(configPath)
		if loadErr != nil {
			log.Warn("config ensure defaults validation failed", "path", configPath, "err", loadErr)
		}
		return configPath, loadErr
	}
	// #nosec G703 -- installer writes the merged agent-gate config to the resolved XDG path.
	if err := os.WriteFile(configPath, []byte(next), 0o600); err != nil {
		log.Warn("config ensure defaults write merged failed", "path", configPath, "err", err)
		return configPath, fmt.Errorf("write merged config: %w", err)
	}
	_, loadErr := LoadExisting(configPath)
	if loadErr != nil {
		log.Warn("config ensure defaults validation failed", "path", configPath, "err", loadErr)
	}
	return configPath, loadErr
}

func canonicalConfigWithUpdateOverride(mode string) string {
	if mode == "" || mode == UpdateModeApply {
		return defaultConfigTOML
	}
	return strings.Replace(defaultConfigTOML, defaultUpdateBlock(), updateBlockForMode(mode), 1)
}

func mergeUpdateDefaults(contents string, mode string) (string, bool, error) {
	log := slog.Default()
	var decoded Config
	err := toml.Unmarshal([]byte(contents), &decoded)
	if err != nil {
		log.Warn("config update merge decode failed", "err", err)
		return "", false, fmt.Errorf("decode config before merge: %w", err)
	}
	// Whether the file already declares an [update] table decides between
	// merging into it and appending a fresh one, so an absent table and a table
	// present with default values must stay distinguishable. The previous
	// library answered this with MetaData.IsDefined; go-toml has no equivalent,
	// so the source text is scanned for the table header instead, using the same
	// matcher the rewrite below uses to find it.
	hasUpdate := topLevelTableDeclared(contents, "update")
	if hasUpdate && mode == "" {
		return contents, false, nil
	}
	if !hasUpdate {
		block := updateBlockForMode(mode)
		separator := "\n"
		if strings.HasSuffix(contents, "\n") {
			separator = ""
		}
		return contents + separator + "\n" + block, true, nil
	}
	block := mergedUpdateBlock(decoded.Update, mode)
	next, replaced := replaceTopLevelTable(contents, "update", block)
	if !replaced {
		err := fmt.Errorf("update table was detected but could not be replaced")
		log.Warn("config update merge replace failed", "err", err)
		return "", false, err
	}
	return next, next != contents, nil
}

func updateBlockForMode(mode string) string {
	enabled := "true"
	resolvedMode := mode
	if resolvedMode == "" {
		resolvedMode = UpdateModeApply
	}
	if resolvedMode == "off" {
		enabled = "false"
		resolvedMode = UpdateModeApply
	}
	return fmt.Sprintf(`[update]
enabled = %s
mode = %q
interval = "24h"
repo = "agoodkind/agent-gate"
allow_prerelease = true
`, enabled, resolvedMode)
}

func defaultUpdateBlock() string {
	return updateBlockForMode(UpdateModeApply)
}

func mergedUpdateBlock(existing Update, mode string) string {
	enabled := existing.Enabled == nil || *existing.Enabled
	resolvedMode := existing.Mode
	if resolvedMode == "" {
		resolvedMode = UpdateModeApply
	}
	interval := existing.Interval
	if interval == "" {
		interval = "24h"
	}
	repo := existing.Repo
	if repo == "" {
		repo = DefaultUpdateRepo
	}
	allowPrerelease := true
	if existing.AllowPrerelease != nil {
		allowPrerelease = *existing.AllowPrerelease
	}
	if mode == "off" {
		enabled = false
		resolvedMode = UpdateModeApply
	} else if mode != "" {
		enabled = true
		resolvedMode = mode
	}
	return fmt.Sprintf(`[update]
enabled = %t
mode = %q
interval = %q
repo = %q
allow_prerelease = %t
`, enabled, resolvedMode, interval, repo, allowPrerelease)
}

func replaceTopLevelTable(contents string, tableName string, replacement string) (string, bool) {
	lines := strings.SplitAfter(contents, "\n")
	start := -1
	end := len(lines)
	for i := range lines {
		if isTopLevelTableHeader(lines[i], tableName) {
			start = i
			continue
		}
		if start >= 0 && i > start && strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	if start < 0 {
		return contents, false
	}
	var merged strings.Builder
	for i := range start {
		merged.WriteString(lines[i])
	}
	if merged.Len() > 0 && !strings.HasSuffix(merged.String(), "\n") {
		merged.WriteByte('\n')
	}
	merged.WriteString(replacement)
	for i := end; i < len(lines); i++ {
		merged.WriteString(lines[i])
	}
	return merged.String(), true
}

// topLevelTableDeclared reports whether the TOML source declares the named
// top-level table at all, which is a different question from whether its fields
// hold non-zero values: a decode into a typed struct cannot tell an absent
// table from a table of zero values.
func topLevelTableDeclared(contents string, tableName string) bool {
	for line := range strings.SplitSeq(contents, "\n") {
		if isTopLevelTableHeader(line, tableName) {
			return true
		}
	}
	return false
}

// isTopLevelTableHeader reports whether one source line opens the named
// top-level table.
//
// TOML allows whitespace inside the brackets and a trailing comment, and a key
// may be quoted, so an exact string comparison against "[update]" misses
// "[update] # managed", "[ update ]", and "[\"update\"]". Missing the header
// made mergeUpdateDefaults append a second [update] table and produce a config
// that no longer parses. Both the presence check and the rewrite use this, so
// they cannot disagree about which line is the header.
func isTopLevelTableHeader(line string, tableName string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return false
	}
	// An array-of-tables header opens with [[ and is a different construct.
	if strings.HasPrefix(trimmed, "[[") {
		return false
	}
	closing := strings.Index(trimmed, "]")
	if closing < 0 {
		return false
	}
	// Only a comment may follow the closing bracket; anything else is not a
	// bare table header.
	rest := strings.TrimSpace(trimmed[closing+1:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return false
	}
	key := strings.TrimSpace(trimmed[1:closing])
	key = strings.Trim(key, `"'`)
	return key == tableName
}
