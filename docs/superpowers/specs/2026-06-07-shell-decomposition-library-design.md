# Shell command decomposition library (shelldecomp): design

## Context

agent-gate decides allow/block on shell commands, and its current shell handling is a set of hand-rolled regex helpers: `shellFields`, `splitChain`, `ApplyCdChain`/`cdTarget`, `stripHeredocBodies`, and the `ShellReadSpec` driven `ExtractReadTargets`, plus derived fields like `cmd_segments` and `cmd_double_hyphen_prose`. These approximate a parser and leak at every seam. This session alone produced several false positives traceable to those seams:

- `cd "$VAR" && grep -rn X .` keyed the grep on the payload cwd, because `BaseCWD()` ignores the cd chain and `cdTarget` joins the literal `$VAR`. The grep actually ran in a different repo that agent-gate could not resolve.
- `grep` inside a heredoc body was treated as a live command, and pipe-segment scoping was ad hoc.
- Unexpandable operands (`$tea_dir/tea.go`) had to be special-cased by string-scanning for `$`.

The shared root cause is that agent-gate reasons about shell text with regex instead of a real syntax tree. The intended outcome is a reusable library that parses any shell command into a real tree, recurses into embedded languages, and classifies every part into machine-meaningful categories, so consumers (agent-gate first) ask structured questions instead of pattern-matching strings.

## Decisions taken during brainstorming

- **Standalone library, shared grammar foundation.** A new module `shelldecomp` holds the shell-specific logic. The tree-sitter plumbing is extracted from lm-semantic-search into a shared module both repos depend on.
- **Fidelity is hybrid by criticality.** A curated high-value set of grammars is parsed precisely; everything else degrades to a typed `Opaque` node. Unparseable input is a first-class category, not an error.
- **zsh is best-effort via the bash grammar.** No mature zsh tree-sitter grammar exists; zsh-only syntax becomes ERROR nodes that the `Opaque` category absorbs.
- **agent-gate may break config compatibility.** The sole consumer is the author, so the agent-gate migration can replace `ShellReadSpec`, the `cmd_read_targets` field, and `cache_key` outright rather than preserve the old schema.

## Prior art to reuse (from lm-semantic-search)

The `internal/splitter` package already solves the hard plumbing:

- Binding `github.com/tree-sitter/go-tree-sitter` v0.25.0, with the CGO and build story worked out.
- Grammars pinned and importable as public modules: bash, python, javascript/typescript, go, rust, c, cpp, java, php, ruby, scala, kotlin, objc, json, html, css. `tree_sitter_bash.Language()` is ready.
- Patterns: the `grammarForLanguage` registry and `extensionLanguages` map (`internal/splitter/splitter.go`), the grammar-agnostic recursive walker (`chunkNode` / `mergeChildChunks` / `nonWhitespaceByteCount`), and the vendoring kit for grammars not on go.mod (the `binding.go` CGO shim plus `grammar_parser.c` / `grammar_scanner.c`, the Makefile `grammars` target, and `scripts/install-tree-sitter.sh`).

The one capability lms does not use is tree-sitter's Query / injection API, even though Swift's `upstream/queries/injections.scm` shows the format. Injection queries are exactly the recursive-embedded-language mechanism, so that is the part shelldecomp builds.

## Architecture

### Module and dependency graph

```
tree-sitter-foundation   (extracted from lms: binding, grammar registry,
        |                 extension->lang map, vendoring kit + Makefile)
        |-- lm-semantic-search   (splitter refactored to consume it; no behavior change)
        \-- shelldecomp          (host parse + inject/dispatch recursion + classifier)
                  \-- agent-gate (retires shellFields/splitChain/ApplyCdChain/ShellReadSpec)
```

### Pipeline: one command string into a classified tree

1. **Host parse** with tree-sitter-bash into a CST. zsh-only syntax yields ERROR nodes.
2. **Embedding pass (hybrid, both mechanisms are required):**
   - *Injection queries* (`.scm`) for in-grammar literals the host grammar already isolates: heredoc bodies, regex literals, here-strings. Each names a sub-grammar to recurse into.
   - *Dispatch table* for calling conventions injections cannot model, because the bash grammar sees `python -c "code"` as a command with a plain string argument. The table maps `{argv0, subcommand, flag} -> {language, code span}` and covers `bash -c`, `ssh host '...'`, `find -exec ... ;`, `xargs`, `python`/`node`/`ruby`/`perl -c`/`-e`, and `awk`/`sed`/`jq` programs. Wrappers (`env`, `sudo`, `time`, `timeout`, `nohup`, `stdbuf`) unwrap and re-dispatch.
   - No grammar, or a parse error, produces an `Opaque` node carrying the raw span and a best-guess language.
3. **cwd / scope pass:** a cwd stack walked over the list, pipe, and subshell structure. `cd` updates the cwd when its target is resolvable and poisons it to `UNRESOLVABLE` when not. A subshell `( )`, a `bash -c`, and an `ssh` remote string each open a fresh scope. Heredoc bodies are data, not commands run in the current shell.
4. **Classification pass:** tag each command and word per the taxonomy below, then resolve path operands against the scope cwd.

### Node taxonomy: the categorically classifiable parts

A uniform node spans every language in the tree:

```
Node {
  Lang        // shell | python | regex | sql | awk | opaque | ...
  Kind        // category (below)
  Span        // byte range in the original top-level string
  Text        // raw slice
  Resolvable  // all-literal, no unexpandable parts
  Value       // resolved string when Resolvable (e.g. a path)
  Children    // []Node, heterogeneous across languages
}
```

Kinds:

- **Command:** nav | read | write | search | vcs | network | build | pkg | interpreter | wrapper | unknown.
- **Word:** flag | flag-value | read-path | write-path | pattern(regex) | embedded-code | literal | unresolvable-expansion.
- **Redirect:** read | write | heredoc(lang-tagged data).
- **Scope:** each command is annotated with an effective cwd of `{value | UNRESOLVABLE}`.

### Consumer API

A small query surface over the classified tree:

- `ReadTargets() []PathRef` where `PathRef` carries the path, resolvability, resolved value, owning command, and the scope cwd state.
- `WriteTargets()`, `Commands()`, `EmbeddedRegions()`, `EffectiveCwdAt(node)`.

agent-gate's grep gate becomes "give me `ReadTargets` with resolvability and scope cwd." `cd "$VAR" && grep .` then falls out as `UNRESOLVABLE`, so the gate fails open by construction rather than by a string-scan patch.

## Build order

- **Phase 0 (enabler):** extract `tree-sitter-foundation` from lms and refactor the lms splitter onto it. Pure refactor, no behavior change. This is the riskiest coupling, so it lands and is verified first.
- **Phase 1 (first real slice):** shelldecomp core: bash host parse, cwd/scope model, path-resolvability classification, and the `ReadTargets` / `EffectiveCwdAt` API. Embedded code is `Opaque` at this stage. This alone replaces agent-gate's hand-rolled parsing and removes the known false positives.
- **Phase 2:** turn on injection plus dispatch recursion into the curated grammars.
- **Phase 3:** broaden the taxonomy and language set; agent-gate fully retires the `ShellReadSpec` regex specs and redesigns the affected config (compat break allowed).

The first implementation plan should target Phase 0 plus Phase 1, which delivers the foundation and the agent-gate bug fix together. Phases 2 and 3 become follow-on specs and plans.

## Verification

- **Phase 0:** lms `make test` and `make lint` stay green, and indexing behavior is unchanged (same chunks for a fixed corpus), confirming the extraction is behavior-preserving.
- **Phase 1:** unit tests for shelldecomp over the real false-positive commands from this session, asserting the classified output:
  - `cd "$VAR" && grep -rn X .` yields a read command whose scope cwd is `UNRESOLVABLE`, so `ReadTargets` are unresolvable and the gate would allow.
  - `cd /Users/.../indexed-repo && grep -rn X .` yields a resolved scope cwd under the indexed repo, so the gate would block.
  - `grep` inside a `cat <<'EOF' ... EOF` heredoc is classified as heredoc data, not a live command.
  - `git grep`, a `/tmp` log grep, and a `find ... | grep` stdin pipe each classify as expected.
- **Phase 1 integration:** wire agent-gate's exec gate to shelldecomp behind the existing rule, run `make test` / `make lint`, deploy, and replay the recorded commands through the daemon, confirming each decision in `~/.local/state/agent-gate/sqlite/audit.db`.
- **Phases 2 and 3:** per-language injection tests (regex-in-grep, python-in-`-c`, sql-in-heredoc, shell-in-`ssh`), and an agent-gate config-migration test once the old specs are retired.

## Open questions for the implementation plan

- The exact home and module path of `tree-sitter-foundation` and `shelldecomp` (new repos vs a shared monorepo path).
- Whether agent-gate depends on the foundation directly for grammar availability, or only transitively through shelldecomp.
- The precise dispatch-table format and how operators (here, the author) extend it.
