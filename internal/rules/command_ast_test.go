package rules

import (
	"strings"
	"testing"
)

// TestRenderCommandASTPlainRead confirms a bare read command renders its
// argv0 and read target, with no write section, so the judge sees the
// command touches only go.mod.
func TestRenderCommandASTPlainRead(t *testing.T) {
	rendered := renderCommandAST("cat go.mod", "/repo", "/home/user")

	if !strings.Contains(rendered, "cat") {
		t.Fatalf("rendered output missing argv0 cat: %q", rendered)
	}
	if !strings.Contains(rendered, "go.mod") {
		t.Fatalf("rendered output missing read target go.mod: %q", rendered)
	}
	if strings.Contains(rendered, "writes:") {
		t.Fatalf("rendered output should have no write section: %q", rendered)
	}
}

// TestRenderCommandASTPipe confirms both stages of a pipeline are rendered,
// so the judge sees the full data flow rather than only the first command.
func TestRenderCommandASTPipe(t *testing.T) {
	rendered := renderCommandAST("grep -rn foo . | head", "/repo", "/home/user")

	if !strings.Contains(rendered, "grep") {
		t.Fatalf("rendered output missing argv0 grep: %q", rendered)
	}
	if !strings.Contains(rendered, "head") {
		t.Fatalf("rendered output missing argv0 head: %q", rendered)
	}
}

// TestRenderCommandASTRedirectWrite confirms an output redirect surfaces a
// write target, so the judge sees the command modifies README.md even
// though "write" never appears in the verbatim text as a tool name.
func TestRenderCommandASTRedirectWrite(t *testing.T) {
	rendered := renderCommandAST("echo x > README.md", "/repo", "/home/user")

	if !strings.Contains(rendered, "writes:") {
		t.Fatalf("rendered output missing writes section: %q", rendered)
	}
	if !strings.Contains(rendered, "README.md") {
		t.Fatalf("rendered output missing write target README.md: %q", rendered)
	}
}

// TestRenderCommandASTEmbeddedRegion confirms a bash -c body is rendered as
// an embedded region and its inner command and read target are surfaced, so
// a search or write laundered through a nested shell stays visible to the
// judge instead of hiding behind an opaque -c string.
func TestRenderCommandASTEmbeddedRegion(t *testing.T) {
	rendered := renderCommandAST(`bash -c 'cat secret.txt'`, "/repo", "/home/user")

	if !strings.Contains(rendered, "embedded[shell]") {
		t.Fatalf("rendered output missing embedded shell region: %q", rendered)
	}
	if !strings.Contains(rendered, "cat") {
		t.Fatalf("rendered output missing inner argv0 cat: %q", rendered)
	}
	if !strings.Contains(rendered, "secret.txt") {
		t.Fatalf("rendered output missing inner read target secret.txt: %q", rendered)
	}
}

// TestRenderCommandASTHeredocEmbeddedRegion confirms a heredoc body fed to an
// interpreter is rendered as an embedded region with its inner content
// surfaced, covering the other laundering vector alongside -c bodies.
func TestRenderCommandASTHeredocEmbeddedRegion(t *testing.T) {
	rendered := renderCommandAST("bash <<'EOF'\ncat secret.txt\nEOF", "/repo", "/home/user")

	if !strings.Contains(rendered, "embedded[shell]") {
		t.Fatalf("rendered output missing embedded shell region: %q", rendered)
	}
	if !strings.Contains(rendered, "secret.txt") {
		t.Fatalf("rendered output missing heredoc inner content secret.txt: %q", rendered)
	}
}

// TestRenderCommandASTCwdSuffix confirms a command that runs after a cd
// renders its effective directory when that differs from the top-level cwd,
// so the judge sees where a later command actually reads or writes even
// though the verbatim text only shows a relative path. It also exercises the
// renderer with a cwd other than the default fixture, so the resolved read
// target reflects the post-cd directory.
func TestRenderCommandASTCwdSuffix(t *testing.T) {
	rendered := renderCommandAST("cd sub && cat x", "/work", "/root")

	if !strings.Contains(rendered, "[cwd: /work/sub]") {
		t.Fatalf("rendered output missing post-cd cwd suffix: %q", rendered)
	}
	if !strings.Contains(rendered, "/work/sub/x") {
		t.Fatalf("rendered output missing cd-resolved read target: %q", rendered)
	}
}

// TestRenderCommandASTOpaque confirms an unparseable command returns the
// short marker rather than panicking or echoing the verbatim command, since
// the caller already shows the verbatim text separately.
func TestRenderCommandASTOpaque(t *testing.T) {
	rendered := renderCommandAST("", "/repo", "/home/user")

	if rendered != "unparseable command; judge from the verbatim text" {
		t.Fatalf("rendered = %q, want the unparseable marker", rendered)
	}
}

// TestRenderCommandASTOpaqueNeverPanics confirms a battery of malformed
// shell strings never panics, regardless of whether gksyntax classifies them
// as opaque or tolerates them as valid syntax.
func TestRenderCommandASTOpaqueNeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"   ",
		`echo "unterminated`,
		"$(",
		"| | |",
		"((( unbalanced",
		"\x00\x01\x02",
	}
	for _, input := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("renderCommandAST panicked on %q: %v", input, r)
				}
			}()
			renderCommandAST(input, "/repo", "/home/user")
		}()
	}
}

// TestRenderCommandASTUnresolvedMarker confirms an unresolvable path renders
// a readable marker rather than the raw gksyntax sentinel bytes.
func TestRenderCommandASTUnresolvedMarker(t *testing.T) {
	rendered := renderCommandAST("echo x > $DEST", "/repo", "/home/user")

	if strings.Contains(rendered, "\x00") {
		t.Fatalf("rendered output leaked raw unresolvable sentinel bytes: %q", rendered)
	}
}
