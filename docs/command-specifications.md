# Command specification reference

Conditions can select command arguments, file discovery, prose, and redirection
exemptions through configuration. The parser interprets shell syntax and resolves
paths. Each rule selects its conditions and enforcement action.

## Working directories

The directory selectors expose different stages of command interpretation:

| Selector | Value |
|---|---|
| `cwd` | The hook's literal directory. |
| `base_cwd` | The tool's starting directory, including a recovered Codex workdir, before shell directory changes. |
| `effective_cwd` | The directory after shell directory analysis. |

A validator that parses the complete command uses `base_cwd`. A validator that
receives a resolved target uses that target directly.

## Argument descriptions

The `arguments` table uses the same schema in write, read, prose, enumeration,
and enumeration-consumer descriptions.

Argument fields:

| Field | Interpretation |
|---|---|
| `subcommands` | Literal positional prefix that selects the invocation. |
| `options` | Option aliases, value requirements, and accepted value forms. |
| `short_clusters` | Interpret combined short options. |
| `end_of_options` | Interpret `--` as the end of option parsing. |
| `stop_at_first_positional` | Stop option parsing at the first positional argument. |
| `skip_positionals` | Exclude leading positional arguments from selected operands. |
| `program_options` | Declared options that satisfy one excluded program position. |
| `require_options` | Declared options that must occur for selection. |
| `require_option_values` | Option and literal-value predicates required for selection. |
| `exclude_option_values` | Option and literal-value predicates that prevent selection. |
| `ignore_operand_prefixes` | Literal prefixes of operands to exclude. |
| `operand_stop_tokens` | Literal tokens that end operand collection. |
| `operands_before_options` | Collect only operands before the first option. |
| `unknown_options` | `unresolved` stops later argument classification; `skip` treats undeclared options as valueless. |

Each option declares `names`, `value`, and `forms`. The `value` field accepts
`none`, `required`, or `optional`. The `forms` array accepts `separate`, `attached`,
and `equals`. A valueless option omits `forms`. An optional attached value does
not consume the next argument. The `cwd` setting resolves the option value as a
directory before resolving that command's relative operands.

The optional `attached_value_pattern` field constrains the prefix consumed as an
attached value. The pattern starts with `^`. A clustered option can resume parsing
after that prefix. This supports numeric suffixes without executable-specific
parser branches.

Predicates contain `option` and `values`. Option references must select declared
options. Invalid references, incompatible declarations, and malformed patterns
fail configuration validation.

## Write descriptions

Each `write_specs` entry selects executable basenames with `argv0`. Its
`target_mode` selects `all_operands` or `last_operand`. Its `target_options`
array additionally selects values of declared options as targets.

An explicit matching descriptor replaces common argument interpretation for that
invocation. It preserves independent redirection and analyzed-program targets.
Common descriptions use the same configuration schema.

The legacy `skip_flags_with_values`, `cwd_flags`, and `end_of_options` fields
remain available. The parser normalizes them into argument descriptions.

Unresolved selection remains local to each rule:

| Setting | Effect |
|---|---|
| Descriptor `unresolved = "match"` | Include an unresolved fact from that descriptor. |
| Descriptor `unresolved = "ignore"` | Omit that descriptor's unresolved fact. |
| `shell_write` condition `match_unresolved = true` | Match unresolved facts as well as configured path globs. |
| `shell_write` condition `match_unresolved = false` | Match resolved paths only. |

The descriptor default is `match`. The condition default is `true`. A rule's
action determines whether a matching condition blocks or records an audit result.
The `cmd_write_targets` selector returns resolved paths only.

## Read and enumeration descriptions

A `read_specs` entry can select operands with `arguments`. The legacy positional,
nested-command, and remote-source descriptions remain available separately.
Configuration cannot combine those legacy settings with `arguments` in one entry.
Independent input redirections remain read targets.

The `search_tools` field selects content readers. The `enumeration_specs` field
selects directory discovery independently. Each enumeration entry declares
`argv0`, `arguments`, and `target_mode`, which accepts `all_operands` or `cwd`.
The `default_cwd` setting selects the command directory when an invocation omits
directory operands.

An enumeration entry with `require_content_consumer = true` requires a configured
content reader. Its `consumer_flags` select option values that invoke readers.
Its `consumer_wrappers` describe executable basenames and argument interpretation
for consumers in the same parsed pipeline. The independent `recursive_globs`
setting selects recursive shell-style glob discovery.

## Prose and redirection policies

A condition's `prose_policy` contains `path_patterns`, `commands`, and `heredocs`.
Each command entry declares `argv0`, `arguments`, and either `value_options`,
`operands`, or both. The `require_output_path` setting requires an output path
that matches a configured prose pattern.

A condition's `redirection_policy` contains `ignore_paths` and `ignore_fds`.
These arrays select exempt output paths and descriptor duplications.

An omitted prose or redirection policy uses the shipped configuration data.
An explicit empty policy replaces that data. Policies do not combine implicitly
across conditions.
