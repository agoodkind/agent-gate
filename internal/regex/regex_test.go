package regex

import (
	"strings"
	"sync"
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

func TestFindAllStringIndex(t *testing.T) {
	re := MustCompile(`x+`)
	got := re.FindAllStringIndex("alpha xx beta x gamma xxx delta", -1)
	want := [][2]int{{6, 8}, {14, 15}, {22, 25}}
	if len(got) != len(want) {
		t.Fatalf("expected %d matches, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestFindAllStringIndexHonorsKeepReset(t *testing.T) {
	re := MustCompile(`\b[A-Za-z]+\s+\K--(?=\s+[A-Za-z]+)`)
	subject := "word -- next"

	got := re.FindAllStringIndex(subject, -1)
	want := [][2]int{{5, 7}}
	if len(got) != len(want) {
		t.Fatalf("expected %d matches, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestFindAllStringGroupIndex(t *testing.T) {
	re := MustCompile(`prefix (bad) suffix`)
	subject := "prefix bad suffix and prefix bad suffix"

	got := re.FindAllStringGroupIndex(subject, -1, 1)
	want := [][2]int{{7, 10}, {29, 32}}
	if len(got) != len(want) {
		t.Fatalf("expected %d matches, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestFindAllStringGroupIndexSkipsUnsetGroup(t *testing.T) {
	re := MustCompile(`(a)|(b)`)

	got := re.FindAllStringGroupIndex("ab", -1, 2)
	want := [][2]int{{1, 2}}
	if len(got) != len(want) {
		t.Fatalf("expected %d matches, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestForEachStringGroupIndexStopsEarly(t *testing.T) {
	re := MustCompile(`x+`)
	subject := "x xx xxx xxxx"
	var got [][2]int

	re.ForEachStringGroupIndex(subject, -1, 0, func(start int, end int) bool {
		got = append(got, [2]int{start, end})
		return len(got) < 2
	})

	want := [][2]int{{0, 1}, {2, 4}}
	if len(got) != len(want) {
		t.Fatalf("expected %d matches, got %#v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestRegexpConcurrentMatchAndGroupAccess(t *testing.T) {
	re := MustCompile(`(?i)(alpha)\s+(one|two|three)`)
	const workerCount = 32
	const iterations = 100

	var wg sync.WaitGroup
	errs := make(chan string, workerCount)
	for range workerCount {
		wg.Go(func() {
			for range iterations {
				if !re.MatchString("alpha one") {
					errs <- "MatchString returned false"
					return
				}
				got := re.FindAllStringGroupIndex("alpha one && alpha two", -1, 2)
				if len(got) != 2 {
					errs <- "FindAllStringGroupIndex returned wrong count"
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
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
