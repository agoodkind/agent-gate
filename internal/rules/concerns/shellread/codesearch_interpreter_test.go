package shellread

import (
	"slices"
	"testing"
)

// TestExtractCodeSearchTargetsRubyGlob covers a Ruby program that reads files
// found by Dir.glob. The glob root becomes a code-search target when the rule
// declares ruby.
func TestExtractCodeSearchTargetsRubyGlob(t *testing.T) {
	const cwd = "/repo"
	command := `ruby -e "Dir.glob('/repo/lib/**/*').each { |path| File.read(path) }"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"ruby"}, nil))
	if !slices.Equal(got, []string{"/repo/lib"}) {
		t.Fatalf("Ruby glob fold = %v, want [/repo/lib]", got)
	}
}

// TestExtractCodeSearchTargetsRubyToolGate covers the Ruby tool gate. A Ruby
// program's reads fold nothing when the rule does not declare ruby.
func TestExtractCodeSearchTargetsRubyToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `ruby -e "Dir.glob('/repo/lib/**/*').each { |path| File.read(path) }"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(got) != 0 {
		t.Fatalf("without ruby tool = %v, want none", got)
	}
}

// TestExtractCodeSearchTargetsJavaScriptRead covers a Node.js program that
// reads a file with fs.readFileSync. The file becomes a code-search target when
// the rule declares node.
func TestExtractCodeSearchTargetsJavaScriptRead(t *testing.T) {
	const cwd = "/repo"
	command := `node -e "fs.readFileSync('/repo/main.js', 'utf8')"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"node"}, nil))
	if !slices.Equal(got, []string{"/repo/main.js"}) {
		t.Fatalf("JavaScript read fold = %v, want [/repo/main.js]", got)
	}
}

// TestExtractCodeSearchTargetsJavaScriptToolGate covers the JavaScript tool
// gate. A Node.js program's reads fold nothing when the rule does not declare
// node or nodejs.
func TestExtractCodeSearchTargetsJavaScriptToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `node -e "fs.readFileSync('/repo/main.js', 'utf8')"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(got) != 0 {
		t.Fatalf("without node tool = %v, want none", got)
	}
}

// TestExtractCodeSearchTargetsPHPRead covers a PHP program that reads a file
// with file_get_contents. The file becomes a code-search target when the rule
// declares php.
func TestExtractCodeSearchTargetsPHPRead(t *testing.T) {
	const cwd = "/repo"
	command := `php -r "file_get_contents('/repo/main.php');"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"php"}, nil))
	if !slices.Equal(got, []string{"/repo/main.php"}) {
		t.Fatalf("PHP read fold = %v, want [/repo/main.php]", got)
	}
}

// TestExtractCodeSearchTargetsPHPToolGate covers the PHP tool gate. A PHP
// program's reads fold nothing when the rule does not declare php.
func TestExtractCodeSearchTargetsPHPToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `php -r "file_get_contents('/repo/main.php');"`

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(got) != 0 {
		t.Fatalf("without php tool = %v, want none", got)
	}
}
