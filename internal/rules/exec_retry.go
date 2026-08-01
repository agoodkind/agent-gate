package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// runExpandedCommandWithRetry runs one expanded validator command, retrying up
// to c.RetryCount additional times while the attempt returns an errored verdict.
// A transient failure of the external validator (a process error, a nonzero exit
// for a JSON predicate, or unparseable output) is retried; a clean block or
// allow verdict returns immediately. Each errored attempt is logged, so a
// retried transient failure stays visible. The retries run in the validator
// goroutine that already carries the background (non-cancelable) context, so
// they extend the background run rather than the synchronous TimeoutMs budget.
func (r *ExecRuntime) runExpandedCommandWithRetry(
	ctx context.Context,
	ruleName string,
	c *config.Condition,
	command []string,
	stdin []byte,
	env []string,
) execconcern.Verdict {
	attempts := c.RetryCount + 1
	verdict := execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
	for range attempts {
		res, runErr := r.runner.Run(ctx, command, r.backgroundTimeout(), stdin, env)
		verdict = execconcern.Interpret(c, res, runErr)
		if !verdict.Errored {
			return verdict
		}
		r.logExpandedCommandError(ctx, ruleName, c, command, res, runErr)
		if ctx.Err() != nil {
			return verdict
		}
	}
	return verdict
}
