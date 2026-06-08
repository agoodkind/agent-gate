# Shell command decomposition (gksyntax): design

## What it is

`goodkind.io/gksyntax` is one Go module that parses code into real tree-sitter syntax trees and labels the parts. It holds three packages:

- `treesitter`: the tree-sitter grammar registry, the file-extension to language map, and the vendored grammars.
- `chunk`: the cAST code chunker and its recursive-separator fallback, used for search indexing.
- `shelldecomp`: parses a shell command into a labeled tree, tracks the working directory, classifies read and write targets, and recurses into embedded code.

Two repositories consume it: `lm-semantic-search` imports `gksyntax/chunk` for indexing, and `agent-gate` imports `gksyntax/shelldecomp` for the exec gate.

## Why shelldecomp exists

The exec gate needs to know what a shell command reads and writes and in which directory. Regex cannot answer that without leaking: `cd "$VAR" && grep -rn X .` blames the grep on the wrong directory, `grep` inside a heredoc body looks like a live command, and an operand like `$dir/x` needs ad-hoc `$`-scanning. shelldecomp parses instead, so the gate asks structural questions: what paths does this read, what does it write, and what directory is in effect here.

The same tree describes code an agent writes and runs, not only what it reads. A heredoc that creates a file, a `sed -i` edit, and code inside `python -c` are all the agent authoring or running code, so embedded code is first-class: shelldecomp locates it, tags its language, and parses it with the same grammars when one exists.

## Invariants

- Anything shelldecomp cannot parse becomes an `Opaque` node; Parse never panics.
- Any path it cannot pin to a literal is `Unresolvable`; it never fabricates a path. A consumer treats `Unresolvable` as "allow" rather than acting on a guess.
- A program or option-value operand is never a path: awk and sed scripts, jq filters, and `stat -f` formats are skipped; a process substitution `<(...)` is unresolvable.

## Packaging and consumption

gksyntax vendors the dart and swift grammars as its own git submodules, because neither has a usable Go-module binding against the pinned `github.com/tree-sitter/go-tree-sitter` runtime. A Go module zip does not include submodule contents, so gksyntax cannot be a plain `go get` dependency with those grammars. A local `replace` directive is also unavailable, because go-makefile's lint bans local replacements.

The consumption pattern, used by both lms and agent-gate, is therefore: add gksyntax as a git submodule under `third_party/gksyntax`, and consume it through a committed `go.work` workspace rather than a module `require`. A Makefile order-only prerequisite initializes the submodule recursively and generates the swift parser from its pinned grammar definition before any compile. The generated parser stays inside the submodule working tree and is never committed; nothing generated is committed anywhere. The eighteen other grammars are normal Go-module dependencies of gksyntax.

The git submodule URL is `github.com/agoodkind/gksyntax`, since the `goodkind.io/gksyntax` vanity host serves only the `go get` meta tags and not the git protocol. All Go import paths use `goodkind.io/gksyntax`.

## How one shell command becomes a tree

1. Parse with the bash grammar. zsh-only syntax becomes error nodes that an `Opaque` node absorbs.
2. Track the working directory. Walk the command list: `cd` to a literal path updates the cwd, `cd "$VAR"` sets it to `Unresolvable`, and a subshell, a `bash -c`/`ssh`/container body, and a heredoc-fed interpreter each start a fresh child scope whose cd changes do not leak out. Heredoc bodies are data, not commands. Wrappers `env` and `sudo` are stripped, and a program invoked by absolute path classifies by its basename.
3. Find embedded code two ways: tree-sitter injection for heredoc bodies and here-strings, and a dispatch table keyed on the program name for code the grammar cannot mark.
4. Recurse into each embedded region with its grammar, up to a configurable depth limit (default 5; the measured worst case is 4). A region whose language has no grammar is located and tagged but left `Opaque`.
5. Classify each command and operand, and resolve each path against the tracked cwd.

## Public API

- `Parse(command, baseCwd, homeDir) *Decomposition`.
- `ReadTargets()` and `WriteTargets()` return each path with its resolvability, value, owning command, and cwd.
- `Commands()`, `EmbeddedRegions()`, `EffectiveCwdAt(byteOffset)`.
- `Register(argv0, dispatcher)` lets a consumer add a dispatch entry.

agent-gate's grep gate becomes one step: get `ReadTargets`, block on a resolvable indexed path, allow the rest. `cd "$VAR" && grep .` comes back unresolvable, so the gate allows it.

## Dispatch table

The dispatch table supplies the one thing the parser cannot derive: which named programs run code, and where. It is keyed on the program name; its value is a function over the parsed command that returns the child nodes that are code, read from the AST rather than re-scanned text.

Command-embedders run shell and recurse fully: `heredoc` (body, language by consumer), `ssh` (trailing, new scope), `bash`/`sh`/`zsh -c` (flag value, new scope), `xargs` (trailing), `find -exec` (exec range), `docker` (new scope), `parallel` (trailing).

Language-embedders run foreign code, parsed with their grammar when one exists and left `Opaque` otherwise: `python`/`python3 -c` (Python), `node -e`/`-p` (JS), `ruby -e` (Ruby), `perl -e` (Perl), `osascript -e` (AppleScript), `sqlite3` (SQL).

Mini-languages run as their own program: `awk`, `jq`, and `sed`, with `sed -i` and `awk -i inplace` also implying writes.

## Verification

- gksyntax: `make check` green, golangci baseline empty.
- shelldecomp: unit tests over the recorded command shapes (cd-chain scope, heredoc-as-data, git grep labeling, stdin grep, subshell `$1`, tilde, the full dispatch set, the depth cap, the write-redirect cases, and the program-operand fabrication guards).
- lms: `make test` and `make lint` green, indexing unchanged.
- agent-gate: `make check` green, then deploy and confirm gate decisions against the live daemon and the audit DB.
- Whole-corpus replay: every distinct captured command run through shelldecomp must produce no fabricated path and no contract violation.
