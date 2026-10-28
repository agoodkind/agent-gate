package rules

import (
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellargs"
	"goodkind.io/gksyntax/shelldecomp"
)

func extractCommandDoubleHyphenProseWithPolicy(command, cwd string, policy *config.ProsePolicy) []string {
	if policy == nil {
		policy = config.DefaultProsePolicy()
	}
	if patchEnvelopeRe.MatchString(command) {
		return nil
	}
	var chunks []string
	for _, segment := range cmdChainRe.Split(stripHeredocBodies(command), -1) {
		chunks = append(chunks, configuredCommandProse(segment, cwd, policy)...)
	}
	if policy.Heredocs {
		chunks = append(chunks, configuredHeredocProse(command, cwd, policy)...)
	}
	var out []string
	for _, chunk := range chunks {
		cleaned := dropStructuredIdentifierRuns(chunk)
		if strings.Contains(cleaned, "--") {
			out = append(out, cleaned)
		}
	}
	return out
}

func configuredCommandProse(command, cwd string, policy *config.ProsePolicy) []string {
	home, err := ReadUserHomeDir()
	if err != nil {
		home = ""
	}
	decomposition := shelldecomp.Parse(command, cwd, home)
	outputMatches := decompositionWritesProse(decomposition, policy.PathPatterns)
	var out []string
	for _, parsed := range decomposition.Commands() {
		for _, spec := range policy.Commands {
			out = append(out, commandProse(parsed, spec, outputMatches)...)
		}
	}
	return out
}

func commandProse(command shelldecomp.Command, spec config.ProseCommandSpec, outputMatches bool) []string {
	if !slices.Contains(spec.Argv0, filepath.Base(command.Argv0)) || spec.Arguments == nil {
		return nil
	}
	if spec.RequireOutputPath && !outputMatches {
		return nil
	}
	arguments := shellargs.Parse(command, *spec.Arguments)
	if !arguments.Selected || arguments.Unresolved {
		return nil
	}
	var out []string
	for _, option := range spec.ValueOptions {
		for _, value := range arguments.Options[option] {
			if value.Resolvable {
				out = append(out, value.Value)
			}
		}
	}
	if spec.Operands {
		if operands := proseOperands(arguments.Operands); operands != "" {
			out = append(out, operands)
		}
	}
	return out
}

func proseOperands(operands []shelldecomp.Word) string {
	var words []string
	for _, operand := range operands {
		if operand.Resolvable {
			words = append(words, operand.Value)
		}
	}
	return strings.Join(words, " ")
}

func decompositionWritesProse(decomposition *shelldecomp.Decomposition, patterns []string) bool {
	for _, target := range decomposition.WriteTargets() {
		if target.Resolvable && prosePathMatches(target.Path, patterns) {
			return true
		}
		if !target.Resolvable && !strings.ContainsAny(target.Raw, "$`") && prosePathMatches(target.Raw, patterns) {
			return true
		}
	}
	return false
}

func prosePathMatches(path string, patterns []string) bool {
	cleaned := strings.ToLower(strings.Trim(path, "\"'"))
	for _, pattern := range patterns {
		pattern = strings.ToLower(pattern)
		if matched, err := filepath.Match(pattern, cleaned); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(pattern, filepath.Base(cleaned)); err == nil && matched {
			return true
		}
	}
	return false
}

func configuredHeredocProse(command, cwd string, policy *config.ProsePolicy) []string {
	lines := strings.Split(command, "\n")
	var out []string
	for index, line := range lines {
		delimiters := heredocDelimiters(line)
		if len(delimiters) == 0 {
			continue
		}
		decomposition := shelldecomp.Parse(line+"\n"+delimiters[0], cwd, "")
		if !decompositionWritesProse(decomposition, policy.PathPatterns) {
			continue
		}
		var body []string
		for next := index + 1; next < len(lines); next++ {
			if strings.TrimSpace(lines[next]) == delimiters[0] {
				break
			}
			body = append(body, lines[next])
		}
		if len(body) > 0 {
			out = append(out, strings.Join(body, "\n"))
		}
	}
	return out
}
