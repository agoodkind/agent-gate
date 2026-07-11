package rules_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type guardArea string

const (
	guardAreaSearch   guardArea = "search"
	guardAreaWorktree guardArea = "worktree"
)

type expectedDecision string

const (
	expectedAllow expectedDecision = "allow"
	expectedBlock expectedDecision = "block"
)

type guardTruthCase struct {
	ID           string            `json:"id"`
	Guard        guardArea         `json:"guard"`
	Category     string            `json:"category"`
	Command      string            `json:"command"`
	CWD          string            `json:"cwd"`
	Environment  map[string]string `json:"environment,omitempty"`
	IndexedRoots []string          `json:"indexed_roots,omitempty"`
	GitState     *gitStateFixture  `json:"git_state,omitempty"`
	Expected     expectedDecision  `json:"expected"`
	Rationale    string            `json:"rationale"`
	LegacyCase   string            `json:"legacy_case,omitempty"`
}

type gitStateFixture struct {
	PrimaryCheckout string            `json:"primary_checkout"`
	DefaultBranch   string            `json:"default_branch"`
	CurrentWorktree string            `json:"current_worktree"`
	CurrentBranch   string            `json:"current_branch"`
	Worktrees       []worktreeFixture `json:"worktrees"`
}

type worktreeFixture struct {
	Path      string `json:"path"`
	Branch    string `json:"branch"`
	IsPrimary bool   `json:"is_primary"`
}

func TestGuardTruthSetSchemaAndCoverage(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "guard_truth.jsonl"))
	if err != nil {
		t.Fatalf("open guard truth set: %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close guard truth set: %v", err)
		}
	})

	cases, err := loadGuardTruthSet(file)
	if err != nil {
		t.Fatalf("load guard truth set: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("guard truth set is empty")
	}

	requiredCategories := map[string]struct{}{
		"search/block/direct-indexed":                   {},
		"search/block/literal-expansion":                {},
		"search/block/command-substitution":             {},
		"search/block/eval-laundering":                  {},
		"search/block/shell-c-laundering":               {},
		"search/block/xargs-laundering":                 {},
		"search/block/find-exec-laundering":             {},
		"search/block/recursive-indexed":                {},
		"search/block/git-content-search":               {},
		"search/allow/find-name":                        {},
		"search/allow/substring-text":                   {},
		"search/allow/arrow-text":                       {},
		"search/allow/non-indexed":                      {},
		"search/allow/fd-redirection":                   {},
		"search/allow/stdin-filter":                     {},
		"search/allow/version-help":                     {},
		"worktree/block/file-write-primary":             {},
		"worktree/block/shell-write-primary":            {},
		"worktree/block/git-mutation-primary":           {},
		"worktree/block/file-write-default-secondary":   {},
		"worktree/block/shell-write-default-secondary":  {},
		"worktree/block/git-mutation-default-secondary": {},
		"worktree/block/git-dash-c":                     {},
		"worktree/block/ref-force":                      {},
		"worktree/block/ref-delete":                     {},
		"worktree/block/ref-rename":                     {},
		"worktree/block/ref-checkout-reset":             {},
		"worktree/block/ref-switch-reset":               {},
		"worktree/block/ref-update":                     {},
		"worktree/block/ref-local-push":                 {},
		"worktree/allow/git-read-primary":               {},
		"worktree/allow/tmp-redirect":                   {},
		"worktree/allow/feature-file-write":             {},
		"worktree/allow/non-protected-write":            {},
		"worktree/allow/feature-git-work":               {},
		"worktree/allow/reset-current-branch":           {},
		"worktree/allow/benign-text":                    {},
	}
	legacyCases := make(map[string]struct{})
	for i := 1; i <= 19; i++ {
		legacyCases[fmt.Sprintf("search:g%02d", i)] = struct{}{}
	}
	for i := 1; i <= 15; i++ {
		legacyCases[fmt.Sprintf("worktree:w%02d", i)] = struct{}{}
	}
	for _, legacyCase := range []string{
		"search:literal-assignment-indexed",
		"worktree:literal-outside",
		"worktree:literal-primary",
		"worktree:literal-reassigned",
		"worktree:literal-command-substitution",
		"worktree:single-quoted-literal",
	} {
		legacyCases[legacyCase] = struct{}{}
	}
	counts := make(map[string]int)
	seenLegacyCases := make(map[string]struct{})
	foundProtectedEnvironmentCase := false
	foundTemporaryEnvironmentCase := false
	for _, truthCase := range cases {
		delete(requiredCategories, truthCase.Category)
		if truthCase.LegacyCase != "" {
			if _, ok := legacyCases[truthCase.LegacyCase]; !ok {
				t.Errorf("case %q has unexpected legacy_case %q", truthCase.ID, truthCase.LegacyCase)
			}
			if _, ok := seenLegacyCases[truthCase.LegacyCase]; ok {
				t.Errorf("case %q duplicates legacy_case %q", truthCase.ID, truthCase.LegacyCase)
			}
			seenLegacyCases[truthCase.LegacyCase] = struct{}{}
			delete(legacyCases, truthCase.LegacyCase)
		}
		if truthCase.LegacyCase == "worktree:w14" {
			foundProtectedEnvironmentCase = truthCase.Environment["TARGET"] == "/repo/main/f.txt" && truthCase.Expected == expectedBlock
		}
		if truthCase.ID == "worktree-env-target-tmp" {
			foundTemporaryEnvironmentCase = truthCase.Environment["TARGET"] == "/tmp/f.txt" && truthCase.Expected == expectedAllow
		}
		counts[string(truthCase.Guard)+"/"+string(truthCase.Expected)]++
	}

	if len(requiredCategories) != 0 {
		t.Errorf("missing required categories: %v", sortedKeys(requiredCategories))
	}
	if len(legacyCases) != 0 {
		t.Errorf("missing re-adjudicated legacy cases: %v", sortedKeys(legacyCases))
	}
	if !foundProtectedEnvironmentCase {
		t.Error("legacy worktree:w14 must define TARGET=/repo/main/f.txt and block")
	}
	if !foundTemporaryEnvironmentCase {
		t.Error("truth set must allow the same TARGET command when TARGET=/tmp/f.txt")
	}
	for _, key := range []string{"search/block", "search/allow", "worktree/block", "worktree/allow"} {
		if counts[key] == 0 {
			t.Errorf("truth set has no %s cases", key)
		}
	}
}

func TestLoadGuardTruthSetRejectsInvalidRecords(t *testing.T) {
	validSearch := `{"id":"search-valid","guard":"search","category":"search/allow/non-indexed","command":"grep x /tmp/log","cwd":"/repo/main","indexed_roots":["/repo/main"],"expected":"allow","rationale":"The target is outside the indexed root."}`
	validWorktree := `{"id":"worktree-valid","guard":"worktree","category":"worktree/allow/tmp-redirect","command":"echo x > /tmp/x","cwd":"/worktrees/feature","git_state":{"primary_checkout":"/repo/main","default_branch":"main","current_worktree":"/worktrees/feature","current_branch":"feature","worktrees":[{"path":"/repo/main","branch":"main","is_primary":true},{"path":"/worktrees/feature","branch":"feature","is_primary":false}]},"expected":"allow","rationale":"The redirect targets temporary storage."}`

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"malformed JSON", `{`, "decode line 1"},
		{"unknown field", strings.Replace(validSearch, `"expected"`, `"extra":true,"expected"`, 1), "unknown field"},
		{"trailing JSON", validSearch + `{}`, "trailing JSON"},
		{"duplicate id", validSearch + "\n" + validSearch, `duplicate id "search-valid"`},
		{"invalid guard", strings.Replace(validSearch, `"search"`, `"network"`, 1), `invalid guard "network"`},
		{"invalid expected", strings.Replace(validSearch, `"allow"`, `"unknown"`, 1), `invalid expected "unknown"`},
		{"missing indexed roots", strings.Replace(validSearch, `,"indexed_roots":["/repo/main"]`, "", 1), "requires indexed_roots"},
		{"missing git state", strings.Replace(validWorktree, `,"git_state":{`+gitFixtureBody()+`}`, "", 1), "requires git_state"},
		{"cross-guard legacy case", strings.Replace(validSearch, `"rationale":"The target is outside the indexed root."`, `"rationale":"The target is outside the indexed root.","legacy_case":"worktree:w01"`, 1), "does not match guard"},
		{"duplicate worktree path", strings.Replace(validWorktree, `]}`, `,{"path":"/worktrees/feature","branch":"other","is_primary":false}]}`, 1), "duplicate worktree path"},
		{"secondary default category requires secondary default worktree", strings.Replace(strings.Replace(validWorktree, `"worktree/allow/tmp-redirect"`, `"worktree/block/file-write-default-secondary"`, 1), `"expected":"allow"`, `"expected":"block"`, 1), "requires a non-primary default-branch worktree"},
		{"non-portable environment path", strings.Replace(validWorktree, `"cwd":"/worktrees/feature"`, `"cwd":"/worktrees/feature","environment":{"TARGET":"/Users/alice/file"}`, 1), "environment TARGET"},
		{"private path", strings.Replace(validSearch, "/repo/main", "/Users/alice/repo", 1), "not a portable absolute path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := loadGuardTruthSet(strings.NewReader(test.content))
			if err == nil {
				t.Fatal("loadGuardTruthSet() returned nil error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadGuardTruthSet() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func loadGuardTruthSet(reader io.Reader) ([]guardTruthCase, error) {
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	seenIDs := make(map[string]struct{})
	var cases []guardTruthCase
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			return nil, fmt.Errorf("line %d is blank", lineNumber)
		}
		var truthCase guardTruthCase
		decoder := json.NewDecoder(strings.NewReader(string(line)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&truthCase); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", lineNumber, err)
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("decode line %d: trailing JSON value", lineNumber)
			}
			return nil, fmt.Errorf("decode line %d: trailing JSON: %w", lineNumber, err)
		}
		if err := validateGuardTruthCase(truthCase); err != nil {
			return nil, fmt.Errorf("validate line %d: %w", lineNumber, err)
		}
		if _, ok := seenIDs[truthCase.ID]; ok {
			return nil, fmt.Errorf("line %d has duplicate id %q", lineNumber, truthCase.ID)
		}
		seenIDs[truthCase.ID] = struct{}{}
		cases = append(cases, truthCase)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan truth set: %w", err)
	}
	return cases, nil
}

func validateGuardTruthCase(truthCase guardTruthCase) error {
	if truthCase.ID == "" || truthCase.Category == "" || truthCase.Command == "" || truthCase.CWD == "" || truthCase.Rationale == "" {
		return fmt.Errorf("id, category, command, cwd, and rationale are required")
	}
	if truthCase.Guard != guardAreaSearch && truthCase.Guard != guardAreaWorktree {
		return fmt.Errorf("invalid guard %q", truthCase.Guard)
	}
	if truthCase.Expected != expectedAllow && truthCase.Expected != expectedBlock {
		return fmt.Errorf("invalid expected %q", truthCase.Expected)
	}
	wantCategoryPrefix := string(truthCase.Guard) + "/" + string(truthCase.Expected) + "/"
	if !strings.HasPrefix(truthCase.Category, wantCategoryPrefix) {
		return fmt.Errorf("category %q must start with %q", truthCase.Category, wantCategoryPrefix)
	}
	if truthCase.LegacyCase != "" && !strings.HasPrefix(truthCase.LegacyCase, string(truthCase.Guard)+":") {
		return fmt.Errorf("legacy_case %q does not match guard %q", truthCase.LegacyCase, truthCase.Guard)
	}
	if !portableAbsolutePath(truthCase.CWD) {
		return fmt.Errorf("cwd %q is not a portable absolute path", truthCase.CWD)
	}
	if len(truthCase.Rationale) > 160 || strings.Contains(truthCase.Rationale, "\n") {
		return fmt.Errorf("rationale must be one concise line of at most 160 characters")
	}
	for name, value := range truthCase.Environment {
		if name == "" || !portableAbsolutePath(value) {
			return fmt.Errorf("environment %s value %q is not a portable absolute path", name, value)
		}
	}
	if truthCase.Guard == guardAreaSearch {
		if len(truthCase.IndexedRoots) == 0 {
			return fmt.Errorf("search case %q requires indexed_roots", truthCase.ID)
		}
		if truthCase.GitState != nil {
			return fmt.Errorf("search case %q must not include git_state", truthCase.ID)
		}
		for _, root := range truthCase.IndexedRoots {
			if !portableAbsolutePath(root) {
				return fmt.Errorf("indexed root %q is not a portable absolute path", root)
			}
		}
		return nil
	}

	if len(truthCase.IndexedRoots) != 0 {
		return fmt.Errorf("worktree case %q must not include indexed_roots", truthCase.ID)
	}
	if truthCase.GitState == nil {
		return fmt.Errorf("worktree case %q requires git_state", truthCase.ID)
	}
	if err := validateGitState(*truthCase.GitState); err != nil {
		return err
	}
	if strings.HasSuffix(truthCase.Category, "-default-secondary") {
		return validateSecondaryDefaultWorktreeTarget(truthCase)
	}
	return nil
}

func validateSecondaryDefaultWorktreeTarget(truthCase guardTruthCase) error {
	for _, worktree := range truthCase.GitState.Worktrees {
		if worktree.IsPrimary || worktree.Branch != truthCase.GitState.DefaultBranch {
			continue
		}
		if truthCase.CWD == worktree.Path || strings.Contains(truthCase.Command, worktree.Path) {
			return nil
		}
	}
	return fmt.Errorf("category %q requires a non-primary default-branch worktree target", truthCase.Category)
}

func validateGitState(state gitStateFixture) error {
	if !portableAbsolutePath(state.PrimaryCheckout) || !portableAbsolutePath(state.CurrentWorktree) {
		return fmt.Errorf("git state checkout paths must be portable absolute paths")
	}
	if state.DefaultBranch == "" || state.CurrentBranch == "" || len(state.Worktrees) == 0 {
		return fmt.Errorf("git state default_branch, current_branch, and worktrees are required")
	}
	primaryCount := 0
	foundCurrent := false
	seenPaths := make(map[string]struct{})
	for _, worktree := range state.Worktrees {
		if !portableAbsolutePath(worktree.Path) || worktree.Branch == "" {
			return fmt.Errorf("worktree path and branch are required")
		}
		if _, ok := seenPaths[worktree.Path]; ok {
			return fmt.Errorf("duplicate worktree path %q", worktree.Path)
		}
		seenPaths[worktree.Path] = struct{}{}
		if worktree.IsPrimary {
			primaryCount++
			if worktree.Path != state.PrimaryCheckout {
				return fmt.Errorf("primary worktree path must match primary_checkout")
			}
		}
		if worktree.Path == state.CurrentWorktree && worktree.Branch == state.CurrentBranch {
			foundCurrent = true
		}
	}
	if primaryCount != 1 || !foundCurrent {
		return fmt.Errorf("git state worktrees must identify the primary and current worktrees")
	}
	return nil
}

func portableAbsolutePath(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	return !strings.HasPrefix(path, "/Users/")
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func gitFixtureBody() string {
	return `"primary_checkout":"/repo/main","default_branch":"main","current_worktree":"/worktrees/feature","current_branch":"feature","worktrees":[{"path":"/repo/main","branch":"main","is_primary":true},{"path":"/worktrees/feature","branch":"feature","is_primary":false}]`
}
