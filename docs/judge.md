# LLM judge

On every gated tool call the judge shows two language models the same input and lets them reason about what the command is actually doing, rather than matching a fixed set of regexes. gpt-5.4-mini enforces the decision. agent-gate-judge-v4 judges the same input and is recorded for comparison and training, so it never affects the live outcome. The rule's deterministic conditions stay the fail-closed backstop when the judge call is unavailable.

The judge attaches to existing rules that opt in. Each opted-in rule carries a general intent, and one inference call per model per command covers every opted-in rule at once.

## What the judge sees

Both models receive one identical input panel per command. The panel leads with the situation, then presents the thing being judged:

- the chat working directory, the project the conversation is about
- the tool-call directory, shown only when a command runs somewhere other than the chat directory after a `cd`
- the recent conversation tail, fetched from clyde and bounded by a token budget
- the tool name
- the verbatim tool call
- a structural parse of a shell command from gksyntax, surfacing the commands, read targets, write targets, and any embedded regions

A shell command is parsed with gksyntax. A write tool has no shell to parse, so it surfaces its target path and a bounded content snippet instead. The structural parse is best-effort: an unparseable command falls back to a short marker, and the model still has the verbatim text.

The parse is what makes a laundered read or write visible. A search hidden inside a `bash -c` body, a write that arrives through a redirect or `tee`, or a path fed through a heredoc all surface as explicit `reads:` and `writes:` lines even when the verbatim command never spells them out.

### Rendered example

For the Bash command `cd sub && grep -rn TODO . > out.txt` in chat directory `/repo`, with a two-line conversation tail, the input panel reads:

```
chat working directory: /repo
tool-call directory: /repo/sub

recent conversation:
user: find TODOs and save them
assistant: running a search now

tool: Bash

tool call:
cd sub && grep -rn TODO . > out.txt

structure:
cmd:
  cd sub
  grep -rn TODO . [cwd: /repo/sub]
reads:
  /repo/sub (grep)
writes:
  /repo/sub/out.txt (grep)
```

The `writes:` line surfaces the redirect target that the verbatim `>` never names as a write.

## What the judge returns

The output is blocks-only. Each model returns only the rule_ids it decides to block, so a normal command returns an empty list. A rule absent from the list is an allow. Each blocked rule emits its own violation message.

One call per model covers all opted-in rules for the command, so the common allow case returns near-empty output. A reply that fails to parse, or a call that errors or times out, marks every participating rule errored, and each rule then applies its own on-error policy.

## How a verdict folds with the deterministic check

A rule combines the judge verdict with its deterministic condition in one of two modes.

Judge-authoritative rules let the judge decide both directions when its call succeeds. The judge can allow past a deterministic block to clear a false positive, and it can add a block the deterministic condition missed. When the judge call errors, the rule's deterministic conditions decide instead, so deterministic protection still holds when the judge is down. The noisy rules use this mode, for example search, writes, output sampling, and git workflow.

Hard-safety rules keep their deterministic block always. The judge can only add blocks, never allow past. The deterministic condition and the judge verdict join by union, so a block from either one blocks, and the deterministic guard stands even when the judge is unavailable. Secrets, credential reads, signing and baseline tamper guards, and the detection-bypass guards use this mode.

In both modes, a judge outage never removes protection. A judge-authoritative rule falls back to its deterministic conditions, and a hard-safety rule keeps its deterministic block.

## Generality

Each rule's intent states a principle, not an enumeration of tools or command shapes. A wrong case is fixed by a better principle or better context for the judge, never by adding a pattern for that one command. The judge is not tuned against a fixed command list.

## Cost and caching

The design keeps the judge under $10 per month on top of the free tier through a few levers:

- The rule intents lead the prompt as a stable, byte-identical prefix a provider can cache. The per-call variable panel, the conversation tail, the command, and the structural parse, follow as the input.
- The output is near-empty on the common allow case.
- The conversation tail is fetched once per command under a bounded deadline and shared across every rule judged for that command.
- The judge scopes to the tool calls that matter, Bash and write tools.

Cost and cache metrics are queryable per model per day with `agent-gate query cost`. It reads the recorded inference-layer token metadata, deduplicates the batch call's tokens by request id so a shared call counts once, applies the per-model price table, and projects a monthly figure. The price table has priced defaults and accepts `[judge.pricing]` overrides in config.

Over a recorded window of about 3.77 days, billed cloud spend for the enforcing model projects to about $0.17 per month, far under the budget, and the local recorded-only model bills nothing. This figure is a lower bound, because token usage is surfaced for only some calls.

Two measurement gaps are open today:

- The OpenAI prompt-cache hit count is not surfaced in-band. The provider's `cached_tokens` field does not reach agent-gate, so prompt-cache engagement cannot be shown from recorded metadata yet. The cost model already reads the cached path and picks the value up once it is surfaced.
- The agent-gate dedup cache does not currently hit. The per-call conversation tail varies the cache key on every command, so the dedup lever never engages. Cost stays within budget regardless.
