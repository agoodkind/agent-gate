# Shell command decomposition library (shelldecomp): design

## What it is

A Go library that parses a shell command into a real syntax tree and labels each part. agent-gate is the first user.

## Why we need it

agent-gate reads shell commands with regex helpers today: `shellFields`, `splitChain`, `ApplyCdChain`, `stripHeredocBodies`, and `ExtractReadTargets`. Regex is not a parser. It leaks at every seam.

This session hit three false positives from those leaks:

- `cd "$VAR" && grep -rn X .` blamed the grep on the wrong directory. agent-gate could not expand `$VAR`, so it guessed the payload cwd. The grep really ran in another repo.
- `grep` inside a heredoc body was treated as a live command.
- An operand with a variable (`$tea_dir/tea.go`) had to be patched by scanning for `$`.

The fix is to parse, not pattern-match. The library returns a tree. agent-gate asks it questions like "what paths does this read?"

The read gate is the first use, not the only one. The same tree must describe what an agent writes and patches, not only what it reads. A heredoc that creates a file, a `sed -i` edit, a patch envelope, and code inside `python -c` are all the agent authoring or running code. So embedded code content is first-class. The library parses it with the same grammars and classifies it, rather than treating it as a leaf to skip.

## Decisions

- Standalone library. `shelldecomp` holds the shell logic. The tree-sitter setup lives in a shared module.
- Hybrid coverage. Parse a small, curated set of languages well. Everything else becomes an `Opaque` node. Unparseable input is a normal result, not an error.
- zsh is best-effort. There is no good zsh grammar. zsh-only syntax becomes error nodes, which `Opaque` absorbs.
- agent-gate can break its config. The author is the only user. The migration can replace the old config outright.

## Reuse from lm-semantic-search

lms already solved the hard tree-sitter setup in `internal/splitter`:

- Binding: `github.com/tree-sitter/go-tree-sitter` v0.25.0, with CGO and the build wired up.
- Grammars ready to import: bash, python, javascript, typescript, go, rust, c, cpp, java, php, ruby, scala, kotlin, objc, json, html, css.
- Patterns to copy: the grammar registry, the extension map, the tree walker, and the kit for vendoring a grammar (the CGO shim, the Makefile `grammars` target, and `scripts/install-tree-sitter.sh`).

lms does not use tree-sitter's injection queries. shelldecomp adds them. Injection queries are how you recurse into embedded code.

## Architecture

### Modules

```
tree-sitter-foundation   (the tree-sitter setup, lifted from lms)
   |-- lm-semantic-search  (refactored to use it; no behavior change)
   \-- shelldecomp         (parse, recurse, classify)
          \-- agent-gate   (drops its regex helpers)
```

### How one command becomes a tree

1. Parse with the bash grammar. zsh-only syntax becomes error nodes.
2. Find embedded code two ways.
   - Injection queries for code the grammar already marks: heredoc bodies, regex literals, here-strings.
   - A dispatch table for code the grammar cannot mark. The bash grammar sees `python -c "code"` as a command with a string. The table knows `python -c` means the next argument is Python. It covers `bash -c`, `ssh host '...'`, `find -exec`, `xargs`, the `-c` and `-e` interpreters, and `awk`, `sed`, `jq`. Wrappers like `env` and `sudo` are stripped first.
   - Code with no grammar becomes `Opaque`.
   - Recursion goes all the way down. `ssh` into `bash -c` into `find -exec grep` is parsed at every level, not just detected.
3. Track the working directory. Walk the command list. `cd` to a real path updates it. `cd "$VAR"` makes it `UNRESOLVABLE`. A subshell, `bash -c`, or `ssh` starts a fresh directory. Heredoc bodies are data, not commands.
4. Label each command and word. Resolve each path against the tracked directory.

### Node shape

```
Node {
  Lang        // shell | python | regex | sql | opaque | ...
  Kind        // category
  Span        // byte range in the original string
  Text        // raw slice
  Resolvable  // true if all-literal
  Value       // the resolved string when Resolvable
  Children    // []Node
}
```

Kinds:

- Command: nav, read, write, search, vcs, network, build, pkg, interpreter, wrapper, unknown.
- Word: flag, flag-value, read-path, write-path, pattern, embedded-code, literal, unresolvable.
- Redirect: read, write, heredoc.
- Each command also carries its working directory, which is a path or `UNRESOLVABLE`.

### What a user calls

- `ReadTargets()` returns each read path with its resolvability, value, owning command, and directory.
- `WriteTargets()`, `Commands()`, `EmbeddedRegions()`, `EffectiveCwdAt(node)`.

agent-gate's grep gate becomes one step: get `ReadTargets`, block on a resolvable indexed path, allow the rest. `cd "$VAR" && grep .` comes back unresolvable, so the gate allows it.

## Why it cannot blow up

Anything the library cannot parse becomes `Opaque`. Any path it cannot pin down is unresolvable. The user treats unresolvable as allow.

So coverage is always optional. You add a grammar or a dispatch entry only when a real command needs it. Phase 1 parses bash and nothing else.

## Build order

- Phase 0: lift `tree-sitter-foundation` out of lms. Refactor lms to use it. No behavior change. This is the riskiest coupling, so it lands first.
- Phase 1: build the shelldecomp core. Bash only. Track the working directory. Return `ReadTargets`. Embedded code stays `Opaque`. This already removes the false positives.
- Phase 2: add recursion into embedded code, one dispatch entry at a time.
- Phase 3: widen the language set. agent-gate retires its regex config.

The first plan is Phase 0 plus Phase 1.

## Packaging

Two repos, two Go modules, imported with `go get`. agent-gate imports `shelldecomp`. lms imports the foundation. agent-gate gets the grammars through `shelldecomp`.

## Verification

- Phase 0: lms tests and lint stay green. Indexing output is unchanged.
- Phase 1: unit tests over the real commands from this session.
  - `cd "$VAR" && grep -rn X .` returns an unresolvable directory, so the gate allows it.
  - `cd /indexed/repo && grep -rn X .` returns a resolved indexed path, so the gate blocks it.
  - `grep` in a heredoc is data, not a command.
  - `git grep`, a `/tmp` log grep, and `find ... | grep` over stdin classify as expected.
- Phase 1 in agent-gate: wire the gate to shelldecomp, run tests and lint, deploy, replay the recorded commands, and check each decision in the audit DB.
- Phase 2 and 3: per-language recursion tests, and a config-migration test.

## Dispatch table

The dispatch table supplies the one thing the parser cannot derive: which named programs run code, and where. It is keyed on the program name, because program names are strings. Its value is a function that runs on the parsed command and returns the child nodes that are code. The function reads the AST, not re-scanned text. `Lang` is an enum, so a typo fails to compile.

```go
type Embedding struct {
    Node     *Node // the AST node holding the code; span known, quotes stripped
    Lang     Lang  // a registered grammar id, not a free string
    NewScope bool  // fresh cwd: subshell, remote, container
}

type Dispatcher func(cmd *Command) []Embedding

var dispatch = map[string]Dispatcher{} // argv0 -> extractor
```

Common shapes get one-line helpers that build a Dispatcher: `FlagValue`, `Trailing`, `FirstPositional`, `ExecRange`, `Wrapper`. A new shape is a new function, never a framework change. A consumer adds its own with `shelldecomp.Register(argv0, dispatcher)`. That path is rare.

### Seed entries, measured from the audit log

84,426 distinct commands were analyzed. About 7,100 embed code. Nesting reaches four real levels (a heredoc script running `ssh` into `ssh` into `sudo` into `awk`), so recursion must be unbounded.

Command-embedders hide a command, so they need full recursion:

```
heredoc       Body, language by consumer       3,774
ssh           Trailing, Shell, NewScope        2,751
xargs         Trailing, AsCommand                458
find          ExecRange, AsCommand               319
docker        AsCommand, NewScope (container)    162
bash/sh/zsh   FlagValue -c, Shell, NewScope      210
parallel      Trailing, AsCommand                  1
```

Language-embedders hide foreign code. That code is what the agent runs or writes, so it is parsed and classified with the same grammars, not skipped:

```
python3/python  FlagValue -c, Python    1,696
osascript       FlagValue -e, AppleScript 195
perl            FlagValue -e, Perl         111
node            FlagValue -e/-p, JS         74
ruby            FlagValue -e, Ruby          55
sqlite3         FirstPositional, SQL         2
```

Mini-languages run as their own program: `awk` (1,768), `jq` (904), `sed` (real subset). Parse these for their content. `sed -i` and `awk` also write files, so they are edits, not only reads.

Phase 2 starts with `heredoc` and `ssh`, because those two create the depth.

## Interim coverage shipped ahead of the library (2026-06-07)

A live session laundered repo-wide code discovery past the `grep-code-use-semantic-search` gate with `find . -name '*.swift' | xargs grep -l X`. The searcher reads stdin, so `ExtractReadTargets` saw no operand and the index-aware validator failed open. This is exactly the `find ... | grep` over-stdin seam the library is meant to close, so it became the first concrete read-gate deliverable.

What shipped, all still regex rather than a parser, so it inherits the leaks Phase 1 removes:

- `internal/rules/concerns/shellread/codesearch_enum.go`: `enumeratorCodeSearchTargets` attributes the enumerated directory as the code-search target for `find|fd|git ls-files` piped into a searcher, `find -exec grep`, and bare `find -name '*.<codeext>'`. It splits pipelines (only `|`, not `;`/`&&`) so a searcher after a `;` is not blamed on an upstream enumerator. This feeds `cmd_read_targets`, which the existing `grep-code-use-semantic-search` rule's exec validator already keys on, so no second rule or validator was needed. Tests live in `codesearch_test.go` (`TestExtractCodeSearchTargetsEnumerator`).
- The existing rule's regex prefilter gained one alternation, `find|fd ... -i?name '*.<codeext>'`, so a bare code-file enumeration (no searcher token) reaches the validator. The grep-family alternation already admits the enumerator-pipe and `-exec` shapes because they contain a searcher token.

A separate `grep-code-discovery-use-semantic-search` rule and a cwd-aware validator were prototyped and then removed in the same session once the extractor made `cmd_read_targets` accurate, since the target-based validator then covers every shape. When `ReadTargets()` from the library replaces the helper, delete `codesearch_enum.go`, fold its cases into the library's tests, and drop the bare-enumeration alternation back out of the rule's prefilter.
