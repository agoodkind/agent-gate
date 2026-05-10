package diff_test

import (
	"reflect"
	"testing"

	diffconcern "goodkind.io/agent-gate/internal/rules/concerns/diff"
)

func TestNewOnly_EmptyOld(t *testing.T) {
	indexes := [][2]int{{0, 3}, {5, 8}}
	got := diffconcern.NewOnly(indexes, nil, "abcdefgh")
	if !reflect.DeepEqual(got, indexes) {
		t.Fatalf("expected indexes copied through, got %v", got)
	}
}

func TestNewOnly_EmptyNewIndexes(t *testing.T) {
	if got := diffconcern.NewOnly(nil, []string{"x"}, "abc"); got != nil {
		t.Fatalf("expected nil for empty new, got %v", got)
	}
}

func TestNewOnly_FiltersByText(t *testing.T) {
	newText := "first bad and second bad"
	// Two matches for "bad" in newText.
	indexes := [][2]int{{6, 9}, {21, 24}}
	got := diffconcern.NewOnly(indexes, []string{"bad"}, newText)
	if len(got) != 0 {
		t.Fatalf("expected zero matches when same text appears in old, got %v", got)
	}
}

func TestNewOnly_PreservesOrder(t *testing.T) {
	newText := "alpha bad beta worse gamma"
	// Two matches: "bad" and "worse"; old contains only "bad".
	indexes := [][2]int{{6, 9}, {15, 20}}
	got := diffconcern.NewOnly(indexes, []string{"bad"}, newText)
	want := [][2]int{{15, 20}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestNewOnly_RepeatedNewMatchKept(t *testing.T) {
	// "bad" appears twice in new and is not in old at all. Both indexes are kept.
	newText := "bad here and bad there"
	indexes := [][2]int{{0, 3}, {13, 16}}
	got := diffconcern.NewOnly(indexes, []string{"clean"}, newText)
	want := indexes
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
