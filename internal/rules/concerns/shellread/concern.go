// Package shellread parses shell command strings and extracts file paths that
// a command may read into tool output.
package shellread

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"goodkind.io/agent-gate/internal/config"
)

const (
	defaultMaxBytes        = 1048576
	remotePolicyAllow      = "allow"
	remotePolicyBlockAll   = "block_all"
	remotePolicyBlockRisky = "block_risky"
)

// Matcher is the subset of regex behavior used by this condition.
type Matcher interface {
	MatchString(string) bool
}

// FieldAccessor is the subset of rules.FieldSet used by this condition.
type FieldAccessor interface {
	String(selector config.FieldSelector) string
	FilePathValue() string
	BaseCWD() string
}

// MatchResult records one read target that matched the condition.
type MatchResult struct {
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
	Path      string
	Remote    bool
	Reason    string
}

// ReadTarget is one path that a configured command shape may read.
type ReadTarget struct {
	Path   string
	Remote bool
	Spec   string
	Raw    string
}

// EvalShellReadSecretMatches extracts readable paths from the command field,
// probes local files when possible, and returns a match when file content
// matches contentPattern or when probing cannot safely answer for a risky path.
func EvalShellReadSecretMatches(
	fields FieldAccessor,
	commandSelector config.FieldSelector,
	contentPattern Matcher,
	pathPattern Matcher,
	maxBytes int,
	remotePolicy string,
	specs []config.ShellReadSpec,
) []MatchResult {
	if contentPattern == nil || pathPattern == nil {
		return nil
	}
	if commandSelector == config.FieldSelectorInvalid {
		commandSelector = config.FieldToolInputCommand
	}
	command := fields.String(commandSelector)
	if command == "" {
		command = fields.String(config.FieldCommand)
	}
	if command == "" {
		return nil
	}

	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	if remotePolicy == "" {
		remotePolicy = remotePolicyBlockRisky
	}

	targets := ExtractReadTargets(command, fields.BaseCWD(), specs)
	if len(targets) == 0 {
		return nil
	}

	var out []MatchResult
	filePath := fields.FilePathValue()
	for _, target := range targets {
		reason := targetMatchReason(target, contentPattern, pathPattern, maxBytes, remotePolicy)
		if reason == "" {
			continue
		}
		out = append(out, MatchResult{
			FieldPath: "tool_input.command",
			FilePath:  filePath,
			Value:     command,
			Start:     0,
			End:       minLen(command, 1),
			Path:      target.Path,
			Remote:    target.Remote,
			Reason:    reason,
		})
	}
	return out
}

func targetMatchReason(target ReadTarget, contentPattern Matcher, pathPattern Matcher, maxBytes int, remotePolicy string) string {
	if target.Remote {
		return remoteMatchReason(target.Path, pathPattern, remotePolicy)
	}
	if localFileContainsSecret(target.Path, contentPattern, maxBytes) {
		return "content_match"
	}
	if localProbeMiss(target.Path, maxBytes) && matchesPathPattern(target.Path, pathPattern) {
		return "risky_path_unprobed"
	}
	return ""
}

func remoteMatchReason(path string, pathPattern Matcher, remotePolicy string) string {
	switch remotePolicy {
	case remotePolicyAllow:
		return ""
	case remotePolicyBlockAll:
		return "remote_path"
	case "", remotePolicyBlockRisky:
		if matchesPathPattern(path, pathPattern) {
			return "remote_risky_path"
		}
		return ""
	default:
		if matchesPathPattern(path, pathPattern) {
			return "remote_risky_path"
		}
		return ""
	}
}

func localFileContainsSecret(path string, contentPattern Matcher, maxBytes int) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > int64(maxBytes) {
		return false
	}

	reader := io.LimitReader(file, int64(maxBytes)+1)
	content, err := io.ReadAll(reader)
	if err != nil {
		return false
	}
	if len(content) > maxBytes {
		return false
	}
	return contentPattern.MatchString(string(content))
}

func localProbeMiss(path string, maxBytes int) bool {
	info, err := os.Stat(path)
	if err != nil {
		return true
	}
	if !info.Mode().IsRegular() {
		return true
	}
	return info.Size() > int64(maxBytes)
}

func matchesPathPattern(path string, pathPattern Matcher) bool {
	if pathPattern == nil {
		return false
	}
	if pathPattern.MatchString(path) {
		return true
	}
	return pathPattern.MatchString(filepath.Base(path))
}

// ExtractReadTargets parses cmd and returns every configured read target.
func ExtractReadTargets(command, cwd string, specs []config.ShellReadSpec) []ReadTarget {
	if strings.TrimSpace(command) == "" || len(specs) == 0 {
		return nil
	}

	var out []ReadTarget
	for _, segment := range splitChain(command) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		fields := shellFields(segment)
		if len(fields) == 0 {
			continue
		}
		out = append(out, specTargets(fields, segment, cwd, specs)...)
	}
	return out
}

func specTargets(fields []string, raw, cwd string, specs []config.ShellReadSpec) []ReadTarget {
	if len(fields) == 0 {
		return nil
	}
	argv0 := filepath.Base(fields[0])
	var out []ReadTarget
	for _, spec := range specs {
		if !slices.Contains(spec.Argv0, argv0) {
			continue
		}
		out = append(out, inputRedirectionTargets(fields, raw, cwd, spec)...)
		if spec.NestedCommand {
			out = append(out, nestedCommandTargets(fields, raw, cwd, spec, specs)...)
			continue
		}
		if spec.RemoteSources {
			out = append(out, remoteSourceTargets(fields, raw, spec)...)
			continue
		}
		out = append(out, positionalPathTargets(fields, raw, cwd, spec)...)
	}
	return out
}

func inputRedirectionTargets(fields []string, raw, cwd string, spec config.ShellReadSpec) []ReadTarget {
	var out []ReadTarget
	for i := 1; i < len(fields); i++ {
		if !isInputRedirect(fields[i]) {
			continue
		}
		if i+1 >= len(fields) {
			continue
		}
		out = append(out, ReadTarget{
			Path:   resolvePath(cwd, fields[i+1]),
			Remote: false,
			Spec:   spec.Name,
			Raw:    raw,
		})
	}
	return out
}

func isInputRedirect(field string) bool {
	return field == "<" || field == "0<"
}

func nestedCommandTargets(fields []string, raw, cwd string, spec config.ShellReadSpec, specs []config.ShellReadSpec) []ReadTarget {
	start := startIndexForSpec(fields, spec)
	if spec.NestedRemote && len(fields) > start {
		remoteCommand := strings.Join(fields[start:], " ")
		return markRemote(ExtractReadTargets(remoteCommand, cwd, specs), raw)
	}
	if spec.NestedCommandFlag == "" {
		return nil
	}
	for i := 1; i < len(fields); i++ {
		if fields[i] != spec.NestedCommandFlag || i+1 >= len(fields) {
			continue
		}
		return ExtractReadTargets(fields[i+1], cwd, specs)
	}
	return nil
}

func markRemote(targets []ReadTarget, raw string) []ReadTarget {
	for i := range targets {
		targets[i].Remote = true
		targets[i].Raw = raw
	}
	return targets
}

func remoteSourceTargets(fields []string, raw string, spec config.ShellReadSpec) []ReadTarget {
	start := startIndexForSpec(fields, spec)
	if start >= len(fields) {
		return nil
	}
	stop := len(fields) - 1
	var out []ReadTarget
	for i := start; i < stop; i++ {
		field := fields[i]
		if shouldSkipFlagValue(fields, &i, spec) != "" {
			continue
		}
		if field == "--" {
			continue
		}
		if isFlag(field) {
			continue
		}
		if path, ok := remotePath(field); ok {
			out = append(out, ReadTarget{
				Path:   path,
				Remote: true,
				Spec:   spec.Name,
				Raw:    raw,
			})
		}
	}
	return out
}

func positionalPathTargets(fields []string, raw, cwd string, spec config.ShellReadSpec) []ReadTarget {
	start := startIndexForSpec(fields, spec)
	if start >= len(fields) {
		return nil
	}
	var out []ReadTarget
	skippedPositionals := 0
	for i := start; i < len(fields); i++ {
		field := fields[i]
		skippedValueFlag := shouldSkipFlagValue(fields, &i, spec)
		if skippedValueFlag != "" {
			if flagValueCountsAsPositional(skippedValueFlag, spec) && skippedPositionals < spec.SkipPositionals {
				skippedPositionals++
			}
			continue
		}
		if field == "--" {
			continue
		}
		if isFlag(field) {
			continue
		}
		if skippedPositionals < spec.SkipPositionals {
			skippedPositionals++
			continue
		}
		out = append(out, ReadTarget{
			Path:   resolvePath(cwd, field),
			Remote: false,
			Spec:   spec.Name,
			Raw:    raw,
		})
	}
	return out
}

func startIndexForSpec(fields []string, spec config.ShellReadSpec) int {
	for _, flag := range spec.PathArgStartIfFlags {
		if hasFlag(fields, flag) {
			return spec.PathArgStartIfFlagsValue
		}
	}
	return spec.PathArgStart
}

func hasFlag(fields []string, flag string) bool {
	for _, field := range fields[1:] {
		if field == flag {
			return true
		}
		if strings.HasPrefix(field, flag+"=") {
			return true
		}
	}
	return false
}

func shouldSkipFlagValue(fields []string, index *int, spec config.ShellReadSpec) string {
	field := fields[*index]
	for _, flag := range spec.SkipFlagsWithValues {
		if field == flag {
			if *index+1 < len(fields) {
				*index++
			}
			return flag
		}
		if strings.HasPrefix(field, flag+"=") {
			return flag
		}
	}
	return ""
}

func flagValueCountsAsPositional(flag string, spec config.ShellReadSpec) bool {
	return slices.Contains(spec.SkipFlagValuesAsPositionals, flag)
}

func remotePath(value string) (string, bool) {
	if strings.Contains(value, "://") {
		return "", false
	}
	host, path, ok := strings.Cut(value, ":")
	if !ok || host == "" || path == "" {
		return "", false
	}
	return path, true
}

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if strings.HasPrefix(path, "~") {
		return path
	}
	if cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

func isFlag(arg string) bool {
	if arg == "" {
		return false
	}
	if arg == "-" || arg == "--" {
		return false
	}
	return strings.HasPrefix(arg, "-")
}

func splitChain(command string) []string {
	if command == "" {
		return nil
	}
	var segments []string
	var current strings.Builder
	runes := []rune(command)
	var quote rune
	escaped := false
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			current.WriteRune(r)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			current.WriteRune(r)
			quote = r
			continue
		}
		if i+1 < len(runes) {
			next := runes[i+1]
			if (r == '&' && next == '&') || (r == '|' && next == '|') {
				segments = append(segments, current.String())
				current.Reset()
				i++
				continue
			}
		}
		if r == ';' || r == '\n' || r == '|' {
			segments = append(segments, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

func shellFields(s string) []string {
	var fields []string
	var builder strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if builder.Len() == 0 {
			return
		}
		fields = append(fields, builder.String())
		builder.Reset()
	}

	for _, r := range s {
		if escaped {
			builder.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			builder.WriteRune(r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			builder.WriteRune(r)
		}
	}
	flush()
	return fields
}

func minLen(s string, want int) int {
	if len(s) < want {
		return len(s)
	}
	return want
}
