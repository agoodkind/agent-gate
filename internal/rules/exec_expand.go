package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// runExpandedCommands runs one validator invocation per expanded target and
// combines their verdicts into the condition's verdict.
//
// Without for_each there is exactly one command and its verdict is the answer.
// With for_each, match_mode decides how the per-target verdicts combine: "any"
// blocks as soon as one target blocks, and "all" blocks only when every target
// blocks.
func (r *ExecRuntime) runExpandedCommands(
	ctx context.Context,
	ruleName string,
	c *config.Condition,
	commands [][]string,
	stdin []byte,
	env []string,
) execconcern.Verdict {
	if len(commands) == 0 {
		return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
	}

	forEach := c.ForEachSelector().Selector != config.FieldSelectorInvalid
	matchAll := forEach && c.MatchMode == config.ExecMatchAll
	firstBlockMessage := ""
	firstErrored := execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
	sawErrored := false
	for _, command := range commands {
		verdict := r.runExpandedCommandWithRetry(ctx, ruleName, c, command, stdin, env)
		if !forEach {
			return verdict
		}
		if verdict.Errored {
			// Under match_mode = "any" one target the validator cannot classify
			// must not veto the rest. A single unresolvable target would
			// otherwise decide the whole expansion and every remaining target,
			// including an indexed one that would block, is never probed. The
			// error is remembered and only decides the condition when no target
			// blocked. Under match_mode = "all" an errored target still returns
			// immediately, because "every target matches" cannot be proven once
			// one target is unknown.
			if matchAll {
				return verdict
			}
			// A fail-closed rule already blocks on this error, and no later
			// target can change that, so probing the rest only buys a better
			// message. It costs a validator run per remaining target, each up to
			// the background timeout times retry_count, all under a context that
			// cannot be cancelled and while this cache key's singleflight entry
			// is held, so a validator outage would stall every concurrent event
			// sharing the key. The decided verdict is worth more than the message.
			if verdict.Block {
				return verdict
			}
			if !sawErrored {
				sawErrored = true
				firstErrored = verdict
			}
			continue
		}
		if verdict.Block && firstBlockMessage == "" {
			firstBlockMessage = verdict.Message
		}
		if !matchAll && verdict.Block {
			// The returned verdict is a clean block, so it carries Errored=false
			// and the caller records the evaluation as fully classified. That is
			// the right verdict, but it hides that an earlier target was never
			// classified, so the partial failure is logged here rather than
			// disappearing from the record.
			if sawErrored {
				r.log.WarnContext(ctx, "exec validator blocked with an unclassified target",
					"rule", ruleName, "on_error", c.OnError)
			}
			return verdict
		}
		if matchAll && !verdict.Block {
			return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
		}
	}
	if matchAll {
		return execconcern.Verdict{Block: true, Message: firstBlockMessage, Output: "", Errored: false}
	}
	if sawErrored {
		return firstErrored
	}
	return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
}

// logExpandedCommandError records why one expanded validator run produced an
// errored verdict, naming the failure mode so a spawn failure, a nonzero exit
// under a JSON predicate, and unparseable predicate output stay distinguishable
// in the log.
func (r *ExecRuntime) logExpandedCommandError(
	ctx context.Context,
	ruleName string,
	c *config.Condition,
	command []string,
	res execconcern.RunResult,
	runErr error,
) {
	switch {
	case runErr != nil:
		r.log.WarnContext(ctx, "exec validator expanded command errored",
			"rule", ruleName, "on_error", c.OnError, "command", command, "err", runErr)
	case c.BlockOn == config.BlockOnMatch && res.ExitCode != 0:
		r.log.WarnContext(ctx, "exec validator expanded command exited nonzero for JSON match",
			"rule", ruleName, "on_error", c.OnError, "command", command, "exit_code", res.ExitCode)
	case c.BlockOn == config.BlockOnMatch:
		r.log.WarnContext(ctx, "exec validator expanded command returned invalid JSON predicate output",
			"rule", ruleName, "on_error", c.OnError, "command", command)
	default:
		r.log.WarnContext(ctx, "exec validator expanded command produced an errored verdict",
			"rule", ruleName, "on_error", c.OnError, "command", command, "exit_code", res.ExitCode)
	}
}
