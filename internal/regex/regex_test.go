package regex

import (
	"strings"
	"testing"
)

func TestCompileAndMatchUnicode(t *testing.T) {
	re, err := Compile(`\x{2014}`)
	if err != nil {
		t.Fatalf("Compile() error: %v", err)
	}

	if !re.MatchString("This is a dash: " + string(rune(0x2014))) {
		t.Error("expected match for Unicode em dash pattern, got no match")
	}
}

func TestMustCompileMatches(t *testing.T) {
	re := MustCompile(`(?i)git\s+commit`)
	if !re.MatchString("Git   commit") {
		t.Error("expected case-insensitive match for `Git   commit`")
	}
}

func TestSplitAndFindStringSubmatch(t *testing.T) {
	re := MustCompile(`,`)
	parts := re.Split("a,b,c", -1)
	if len(parts) != 3 || parts[0] != "a" || parts[1] != "b" || parts[2] != "c" {
		t.Fatalf("Split produced unexpected output: %#v", parts)
	}

	submatchRe := MustCompile(`^([a-z]+)-([0-9]+)$`)
	match := submatchRe.FindStringSubmatch("item-42")
	if len(match) != 3 || match[1] != "item" || match[2] != "42" {
		t.Fatalf("FindStringSubmatch returned unexpected output: %#v", match)
	}
}

func TestSplitCommandChainOperators(t *testing.T) {
	re := MustCompile(`&&|\|\||;|\n`)
	parts := re.Split("git status && git diff", -1)
	if len(parts) != 2 || parts[0] != "git status " || parts[1] != " git diff" {
		t.Fatalf("Split on shell chain operators: %#v", parts)
	}
}

func TestMatchLimitsRejectPotentialReDos(t *testing.T) {
	pattern := `^(a+)+$`
	// Non-matching subject that triggers catastrophic backtracking before PCRE2 can reject.
	subject := strings.Repeat("a", 200) + "b"

	matched, err := matchWithLimits(pattern, subject, 100, 16)
	if err == nil {
		t.Fatalf("expected matchWithLimits error, got match=%v err=%v", matched, err)
	}

	if matched {
		t.Fatal("expected no match when match limits are exhausted")
	}
}
