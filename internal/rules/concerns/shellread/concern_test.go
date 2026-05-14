package shellread_test

import (
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	shellreadconcern "goodkind.io/agent-gate/internal/rules/concerns/shellread"
)

func testReadSpecs() []config.ShellReadSpec {
	return []config.ShellReadSpec{
		{
			Name:         "plain-readers",
			Argv0:        []string{"cat", "less", "more", "strings"},
			PathArgStart: 1,
		},
		{
			Name:                        "search-readers",
			Argv0:                       []string{"grep", "rg"},
			PathArgStart:                1,
			SkipPositionals:             1,
			SkipFlagsWithValues:         []string{"-e", "--regexp", "-f", "--file", "-g", "--glob"},
			SkipFlagValuesAsPositionals: []string{"-e", "--regexp", "-f", "--file"},
		},
		{
			Name:              "shell-c",
			Argv0:             []string{"bash", "sh", "zsh"},
			NestedCommand:     true,
			NestedCommandFlag: "-c",
		},
		{
			Name:                     "ssh",
			Argv0:                    []string{"ssh"},
			PathArgStart:             2,
			PathArgStartIfFlags:      []string{"-p"},
			PathArgStartIfFlagsValue: 4,
			SkipFlagsWithValues:      []string{"-p", "-i", "-F", "-l"},
			NestedCommand:            true,
			NestedRemote:             true,
		},
		{
			Name:                "remote-copy",
			Argv0:               []string{"scp", "rsync"},
			PathArgStart:        1,
			SkipFlagsWithValues: []string{"-P", "-e", "-i", "-F"},
			RemoteSources:       true,
		},
	}
}

func findTarget(targets []shellreadconcern.ReadTarget, path string, remote bool) (shellreadconcern.ReadTarget, bool) {
	for _, target := range targets {
		if target.Path == path && target.Remote == remote {
			return target, true
		}
	}
	return shellreadconcern.ReadTarget{}, false
}

func TestExtractReadTargets_PlainReader(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets("cat config.toml", "/work", testReadSpecs())
	if _, ok := findTarget(targets, filepath.Join("/work", "config.toml"), false); !ok {
		t.Fatalf("expected config.toml target, got %#v", targets)
	}
}

func TestExtractReadTargets_GrepSkipsPattern(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets("grep -n api_key config.toml", "/work", testReadSpecs())
	if _, ok := findTarget(targets, filepath.Join("/work", "config.toml"), false); !ok {
		t.Fatalf("expected config.toml target, got %#v", targets)
	}
	if _, ok := findTarget(targets, filepath.Join("/work", "api_key"), false); ok {
		t.Fatalf("did not expect grep pattern to be treated as a path: %#v", targets)
	}
}

func TestExtractReadTargets_RgExpressionFlag(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets("rg -e api_key ~/.config/clyde/config.toml", "/work", testReadSpecs())
	if _, ok := findTarget(targets, "~/.config/clyde/config.toml", false); !ok {
		t.Fatalf("expected config path target, got %#v", targets)
	}
	if _, ok := findTarget(targets, filepath.Join("/work", "api_key"), false); ok {
		t.Fatalf("did not expect rg expression value to be treated as a path: %#v", targets)
	}
}

func TestExtractReadTargets_QuotedPipeDoesNotSplitCommand(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets(`rg -n "config.toml|secret|path support" /Users/agoodkind/.codex/memories/MEMORY.md`, "/work", testReadSpecs())
	if _, ok := findTarget(targets, "/Users/agoodkind/.codex/memories/MEMORY.md", false); !ok {
		t.Fatalf("expected MEMORY.md target, got %#v", targets)
	}
}

func TestExtractReadTargets_ShellC(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets(`bash -c "cat ~/.config/clyde/config.toml"`, "/work", testReadSpecs())
	if _, ok := findTarget(targets, "~/.config/clyde/config.toml", false); !ok {
		t.Fatalf("expected nested cat target, got %#v", targets)
	}
}

func TestExtractReadTargets_SSHRemoteCommand(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets(`ssh -p 2222 host 'grep TOKEN ~/.config/clyde/config.toml'`, "/work", testReadSpecs())
	if _, ok := findTarget(targets, "~/.config/clyde/config.toml", true); !ok {
		t.Fatalf("expected remote config target, got %#v", targets)
	}
}

func TestExtractReadTargets_SCPRemoteSource(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets("scp host:~/.aws/credentials .", "/work", testReadSpecs())
	if _, ok := findTarget(targets, "~/.aws/credentials", true); !ok {
		t.Fatalf("expected remote source target, got %#v", targets)
	}
}

func TestExtractReadTargets_InputRedirect(t *testing.T) {
	targets := shellreadconcern.ExtractReadTargets("grep token < secrets.txt", "/work", testReadSpecs())
	if _, ok := findTarget(targets, filepath.Join("/work", "secrets.txt"), false); !ok {
		t.Fatalf("expected input redirect target, got %#v", targets)
	}
}
