# Configure command behavior with shared argument specifications

Configuration controls which command arguments identify files or prose and which
parsed operations a rule includes. Go implements argument parsing, path resolution,
and rule evaluation. A new command description must require only configuration edits.

The user requested generic configuration primitives and an audit of comparable
hardcoding. The user also requested that this work not add automated tests. Validate
the change with existing checks and direct hook evaluation against recorded inputs.

## Preserve the enforcement boundary

The shell parser preserves argument boundaries, quoting, redirections, scope, and
working directories. Language analyzers identify operations in embedded programs.
These mechanisms remain in code.

Configuration supplies command descriptions and selects facts for each rule. The
existing checkout rules continue to decide whether a resolved write target is
protected. A fabricated target must not create an exemption for its command or
suppress a real redirection or embedded-program write.

Unresolved targets retain the existing explicit unresolved result. The strict
`shell_write` condition can reject that result. The `cmd_write_targets` selector
continues to expose only resolved paths. Do not replace uncertainty with a guessed
path or silently reclassify it as a safe operation.

## Replace the identified policy tables

The audit identified these related configuration gaps.

Migration scope:

| Area | Current behavior | Required configuration control |
|---|---|---|
| Write targets | Agent Gate adds editor names, Perl/Ruby activation, and option handling independently of declared `write_specs`. | Declare command arguments and conditional writes in configuration. Apply the configured interpretation to the selected command. |
| Recursive discovery | Any nonempty `search_tools` also enables fixed recursive enumeration behavior for `ls`, `find`, `git ls-files`, and recursive globs. | Select recursive enumeration independently from content readers. Declare command selection and the existing shallow-depth exemption explicitly. |
| Prose extraction | Fixed extensions and `git`/`gh` option lists determine which text the prose selector exposes. | Configure prose path patterns and command arguments that contain prose. |
| Redirection exemptions | Fixed output paths and descriptor numbers disappear before the redirection rule evaluates them. | Configure ignored target paths and descriptor numbers explicitly. |

Fix the existing contract mismatch for `shell_write`: its configuration example
advertises `write_specs`, but validation rejects it and evaluation does not pass
the specifications to extraction. Support the documented capability through the
same extractor used by condition-specific write-target selection.

Do not expand this work into provider protocol mappings, Git reference semantics,
secret classifications, audit storage, or the unrelated opaque-heredoc analysis
heuristic. Record a separately useful finding instead of changing those mechanisms.

## Reuse the existing configuration containers

Extend [ShellWriteSpec](../../../internal/config/write_specs.go) and the existing
read specifications with one shared argument description. Reuse
[condition-specific selector evaluation](../../../internal/rules/fields_condition.go)
for selectors that need rule-owned options.

Keep `write_specs`, `read_specs`, and `search_tools` as the public entry points for
their current jobs. Do not add an expression language, executable configuration,
profile inheritance, or arbitrary callbacks. The builder may introduce a flat
named argument-specification registry if sharing one descriptor between these
containers otherwise requires duplicated command descriptions. References must
resolve during configuration loading and must not cross rule-evaluation contexts.

The builder may choose concrete Go type names and file boundaries. The following
configuration concepts and semantics are required.

Argument operations:

| Concept | Required semantics |
|---|---|
| Command selection | Match configured executable basenames and an optional literal subcommand prefix. Consume the matched prefix before selecting file operands. |
| Option values | Each option declares whether it has no value, requires a value, or accepts an optional value. |
| Accepted value forms | Each option explicitly accepts separate, directly attached, or equals-separated values. An optional attached value never consumes the next argument. |
| Short-option clusters | Enable clustering per descriptor. Stop scanning a cluster when an option consumes its remaining characters. |
| Positional boundary | Configure whether the first positional argument ends option parsing. |
| End of options | Preserve the existing configurable `--` behavior. A following `-leading.txt` is an operand. |
| Excluded positionals | Reuse `skip_positionals` and `skip_flag_values_as_positionals` semantics from read specifications. A program filename or inline program can consume the excluded program position without becoming a target. |
| Working directory | Resolve configured directory-option values before resolving that command's file arguments. |
| Activation | Match required option presence or literal option values against parsed options, never letters inside program text or option values. |
| Target selection | Preserve all/last operand selection. Add option-value, prefixed-operand, or current-directory selection only for a migrated command that requires it. |

Normalize the existing `skip_flags_with_values` and `cwd_flags` declarations into
the shared argument representation. Preserve their established separate-value and
equals-value behavior. Reject conflicting declarations, missing required values,
invalid forms, and unresolved named references with the rule and descriptor in the
error. Do not recover with a built-in command-specific default.

An undeclared option, including an undeclared letter inside a short-option cluster,
produces an unresolved result for that command's argument interpretation. Stop
classifying its later arguments because the unknown option's value requirements
are unavailable. Preserve independent redirection and analyzed-program targets.
Configuration can support the option by declaring it; the engine must not assume
that an unknown option is a boolean flag or guess which argument it consumes.

Configuration chooses the action for unresolved command writes through the existing
strict `shell_write` mechanism and typed checkout conditions. The proposed shipped
default rejects unresolved writes in protected checkouts and requires an explicit
descriptor update. An alternative default records an audit result without blocking.
The user must choose this default before the configuration migration. Both actions
remain configurable; Go must not hardcode either policy.

## Make configured command interpretation authoritative

For each selected rule condition, compute its command-derived targets from its
selected descriptors. Remove Agent Gate's independent editor/interpreter target
append and replace the special-case Perl/Ruby patch with this generic mechanism.
A change to one rule's descriptor must not change another rule's extraction.

Keep structural redirection writes and analyzed embedded-program writes separate
from targets supplied by command arguments. A descriptor cannot suppress those
facts merely by matching the executable that also owns a redirection. If the parser
dependency does not expose the required distinction, add generic origin metadata
to its existing target result. Do not infer origin from a command-name list or
create a second shell parser in Agent Gate. Such metadata must report syntax or
analyzer provenance without changing the dependency's existing command models.

Pass configured command descriptors into the existing bounded extraction of local
embedded commands. Preserve the existing separation of local and remote writes.
Do not scan opaque text as another language to compensate for a missing descriptor.

## Express the Perl and Ruby cases as data

The Perl descriptor activates writes when parsed `-i` is present. Its `-i` option
accepts an optional attached backup suffix. Its `-e` and `-E` options consume program
text. Store the other accepted option values explicitly. The Ruby descriptor defines
its own options. Do not reuse a shared language-specific letter table: Perl `-C`
controls Unicode features, while Ruby `-C` changes directories.

The program exclusion consumes one program position. An inline program satisfies
that exclusion, and a program filename satisfies it when an inline program is
absent. Repeated program options remain program values and never become file
targets.

Required interpretations:

| Command shape | Required result |
|---|---|
| `perl -pi -e 's/a/b/' file.txt` | Select `file.txt`; exclude the separate program value. |
| `perl -p -i.bak -e's/a/b/' file.txt` | Select `file.txt`; consume the backup suffix and attached program. |
| `ruby -pi -rdigest -e'print $_' file.txt` | Select `file.txt`; exclude the library and program values. |
| `perl -i program.pl file.txt` | Exclude the program filename and select `file.txt`. |
| `perl -e 'print "-i"' file.txt` | Do not activate an in-place write from the program text. |
| `perl -pi -e 'print' -- -leading.txt` | Select `-leading.txt`. |
| `perl -- -i file.txt` | Do not interpret the operand `-i` as an option. |

## Configure the other selectors

Recursive discovery receives explicit enumeration descriptors and an independent
recursive-glob selection. The migrated configuration preserves the current
`find -maxdepth 0/1` exclusion as declared policy. A rule that declares `grep` as a
content reader alone must not implicitly select every enumerator.

Prose extraction receives configurable prose path patterns and command prose-value
descriptors. The initial configuration reproduces the current prose extensions and
the current `git commit` and `gh` value options. The shared argument parser extracts
values; it does not decide that particular commands or extensions contain prose.

Redirection extraction receives explicit ignored target paths and descriptor
numbers. Keep operator parsing in code. The initial configuration reproduces the
current stdout/stderr exemptions without inventing additional allowed paths.

## Migrate current configuration without reducing protection

Store command descriptions and selector defaults as configuration data. The removal
of a Go table does not authorize dropping its corresponding protected operation.
Prepare the new configuration and a complete mapping from old behavior to its
declared replacement before replacing the live daemon.

For each migrated selector, compare representative current inputs against both
configurations through hook evaluation. Preserve existing allow/block outcomes
except the verified fabricated-target cases and the user's selected unresolved-option
policy. Check both temporary-file edits and
real protected-checkout edits. Evaluate payloads without executing the described
cleanup operations.

Validate the candidate configuration with the new executable before selecting it
for the supervised service. Treat a required missing descriptor or invalid
reference as a configuration error. Preserve the last valid live configuration
when a replacement fails validation. Do not remove rules, switch them to audit,
or lower their severity to make the migration pass.

Keep the existing installation unchanged until the generic implementation and its
configuration are ready together. Do not restart applications, operate other
agents, or change Clyde. A live Agent Gate deployment must use the repository's
build, install, and supervised deployment commands and verify executable identity,
daemon identity, configuration identity, and the original hook outcome separately.

## Validate without adding tests

Do not add automated test files or a replacement test framework. Run the existing
repository checks. The current build system rejects `make check` and directs
development to `make build`; use the supported build checks and existing test suite.

Use direct hook payload replays for the required interpretations and migrated
selectors. Include quoted options, attached values, optional backup suffixes,
multiple operands, `--`, directory options, subcommands, embedded local commands,
real redirects, and unresolved values. Record which input changed its decision and
why. Verify a protected file still produces a deny decision after the original
temporary-file command produces allow.

Remove temporary verification artifacts after use. Preserve source changes in
logical commits, and classify worktree artifacts through the cleanup workflow
before removing them.
