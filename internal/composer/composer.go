// Package composer combines deterministic rule oracles with lm-review verdicts.
package composer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/oracle"
	"goodkind.io/lm-review/api/judgepb"
)

const judgeTimeout = 4 * time.Second

// Verdict is the composer decision type.
type Verdict = oracle.Verdict

const (
	// Block means the event must be blocked.
	Block = oracle.Block
	// Allow means the event is allowed.
	Allow = oracle.Allow
	// Unknown means the deterministic oracle could not decide.
	Unknown = oracle.Unknown
)

// OracleFunc returns the deterministic oracle verdict and a short reason.
type OracleFunc func(ruleSetID string, command string, cwd string, deps Deps) (Verdict, string)

// JudgeResult is the lm-review verdict and reason.
type JudgeResult struct {
	Verdict Verdict
	Reason  string
}

// JudgeFunc evaluates a command through lm-review.
type JudgeFunc func(
	ruleSetID string,
	command string,
	cwd string,
	deps Deps,
	requiredContext []string,
) (JudgeResult, error)

// RuleSetLister returns required context names by rule set id.
type RuleSetLister func() (map[string][]string, error)

// Authority selects which verdict the composer enforces when the oracle and the
// model both produce one.
type Authority string

const (
	// AuthorityUnion blocks when either the model or the oracle blocks. It is the
	// superset of both blocks, so the model catches launderings the oracle
	// cannot parse and the oracle catches what the model misses. Default.
	AuthorityUnion Authority = "union"
	// AuthorityOracle enforces the oracle verdict and consults the model only
	// when the oracle returns Unknown.
	AuthorityOracle Authority = "oracle"
	// AuthorityLLM enforces the model verdict and falls back to the oracle only
	// when the model has no verdict.
	AuthorityLLM Authority = "llm"
)

// RuntimeOptions configures a composer Runtime.
type RuntimeOptions struct {
	Deps                Deps
	JudgeClient         judgepb.JudgeClient
	JudgeEnabled        bool
	Authority           Authority
	Oracle              OracleFunc
	Judge               JudgeFunc
	RuleSetLister       RuleSetLister
	DisagreementLogPath string
	Now                 func() time.Time
}

// Runtime combines deterministic and lm-review verdicts for rule events.
type Runtime struct {
	deps                Deps
	judgeClient         judgepb.JudgeClient
	judgeEnabled        bool
	authority           Authority
	oracle              OracleFunc
	judge               JudgeFunc
	ruleSetLister       RuleSetLister
	disagreementLogPath string
	now                 func() time.Time
	closers             []io.Closer

	ruleSetMu    sync.Mutex
	ruleSetCache map[string][]string
}

// NewRuntime returns a composer runtime with production defaults where a field
// is not supplied.
func NewRuntime(options RuntimeOptions) *Runtime {
	runtime := &Runtime{
		deps:                options.Deps,
		judgeClient:         options.JudgeClient,
		judgeEnabled:        options.JudgeEnabled,
		authority:           options.Authority,
		oracle:              options.Oracle,
		judge:               options.Judge,
		ruleSetLister:       options.RuleSetLister,
		disagreementLogPath: options.DisagreementLogPath,
		now:                 options.Now,
		closers:             nil,
		ruleSetMu:           sync.Mutex{},
		ruleSetCache:        nil,
	}
	if runtime.authority != AuthorityOracle && runtime.authority != AuthorityLLM {
		runtime.authority = AuthorityUnion
	}
	if runtime.deps.WorktreeState == nil {
		runtime.deps.WorktreeState = oracle.WorktreeState
	}
	if runtime.oracle == nil {
		runtime.oracle = defaultOracle
	}
	if runtime.judge == nil {
		runtime.judge = runtime.defaultJudge
	}
	if runtime.ruleSetLister == nil {
		runtime.ruleSetLister = runtime.defaultRuleSetLister
	}
	if runtime.now == nil {
		runtime.now = time.Now
	}
	return runtime
}

// Close releases runtime-owned client connections.
func (r *Runtime) Close() {
	if r == nil {
		return
	}
	closeAll(r.closers)
}

var defaultRuntime atomic.Pointer[Runtime]

// Decide returns the package-level composer verdict.
func Decide(ruleSetID, command, cwd string) Verdict {
	runtime := defaultRuntime.Load()
	if runtime == nil {
		runtime = NewRuntime(RuntimeOptions{
			Deps:                Deps{Clyde: nil, IndexedRoots: nil, WorktreeState: nil},
			JudgeClient:         nil,
			JudgeEnabled:        false,
			Authority:           AuthorityOracle,
			Oracle:              nil,
			Judge:               nil,
			RuleSetLister:       nil,
			DisagreementLogPath: "",
			Now:                 nil,
		})
	}
	return runtime.Decide(ruleSetID, command, cwd)
}

type oracleOutcome struct {
	verdict   Verdict
	reason    string
	latencyMS float64
}

type judgeOutcome struct {
	result    JudgeResult
	err       error
	latencyMS float64
}

// Decide runs the deterministic oracle and lm-review path concurrently, then
// combines them with oracle-authoritative semantics.
func (r *Runtime) Decide(ruleSetID, command, cwd string) Verdict {
	oracleCh := make(chan oracleOutcome, 1)
	judgeCh := make(chan judgeOutcome, 1)

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("composer oracle goroutine panic", "err", recovered)
				oracleCh <- oracleOutcome{
					verdict:   Block,
					reason:    fmt.Sprintf("oracle_goroutine_panic:%v", recovered),
					latencyMS: 0,
				}
			}
		}()
		r.runOracle(ruleSetID, command, cwd, oracleCh)
	}()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("composer judge goroutine panic", "err", recovered)
				judgeCh <- judgeOutcome{
					result:    JudgeResult{Verdict: Unknown, Reason: ""},
					err:       fmt.Errorf("judge goroutine panic: %v", recovered),
					latencyMS: 0,
				}
			}
		}()
		r.runJudge(ruleSetID, command, cwd, judgeCh)
	}()

	oracleResult := <-oracleCh
	judgeResult := <-judgeCh

	enforced := combineVerdicts(r.authority, oracleResult, judgeResult)
	r.logIfNeeded(ruleSetID, command, cwd, oracleResult, judgeResult, enforced)
	return enforced
}

func (r *Runtime) runOracle(
	ruleSetID string,
	command string,
	cwd string,
	out chan<- oracleOutcome,
) {
	started := r.now()
	result := oracleOutcome{verdict: Unknown, reason: "", latencyMS: 0}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.verdict = Block
			result.reason = fmt.Sprintf("oracle_panic:%v", recovered)
		}
		result.latencyMS = durationMillis(r.now().Sub(started))
		out <- result
	}()
	result.verdict, result.reason = r.oracle(ruleSetID, command, cwd, r.deps)
}

func (r *Runtime) runJudge(
	ruleSetID string,
	command string,
	cwd string,
	out chan<- judgeOutcome,
) {
	started := r.now()
	result := judgeOutcome{
		result:    JudgeResult{Verdict: Unknown, Reason: ""},
		err:       nil,
		latencyMS: 0,
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.err = fmt.Errorf("judge panic: %v", recovered)
			result.result = JudgeResult{Verdict: Unknown, Reason: ""}
		}
		result.latencyMS = durationMillis(r.now().Sub(started))
		out <- result
	}()
	requiredContext := r.requiredContext(ruleSetID)
	result.result, result.err = r.judge(ruleSetID, command, cwd, r.deps, requiredContext)
}

func combineVerdicts(authority Authority, oracleResult oracleOutcome, judgeResult judgeOutcome) Verdict {
	switch authority {
	case AuthorityOracle:
		return combineOracleAuthoritative(oracleResult, judgeResult)
	case AuthorityLLM:
		return combineLLMAuthoritative(oracleResult, judgeResult)
	case AuthorityUnion:
		return combineUnion(oracleResult, judgeResult)
	default:
		return combineUnion(oracleResult, judgeResult)
	}
}

// combineUnion blocks when either the oracle or the model blocks, so the
// enforced blocks are the superset of both. It allows only when at least one
// side affirmatively allows and neither blocks, and fails closed when neither
// side produced a verdict.
func combineUnion(oracleResult oracleOutcome, judgeResult judgeOutcome) Verdict {
	oracleV := oracleResult.verdict
	llmV := Unknown
	if judgeResult.err == nil {
		llmV = judgeResult.result.Verdict
	}
	if oracleV == Block || llmV == Block {
		return Block
	}
	if oracleV == Allow || llmV == Allow {
		return Allow
	}
	return Block
}

// combineOracleAuthoritative enforces the oracle verdict and consults the model
// only when the oracle returns Unknown. Fail closed when neither decides.
func combineOracleAuthoritative(oracleResult oracleOutcome, judgeResult judgeOutcome) Verdict {
	if oracleResult.verdict == Block || oracleResult.verdict == Allow {
		return oracleResult.verdict
	}
	if judgeResult.err != nil {
		return Block
	}
	if judgeResult.result.Verdict == Block || judgeResult.result.Verdict == Allow {
		return judgeResult.result.Verdict
	}
	return Block
}

// combineLLMAuthoritative enforces the model verdict and falls back to the
// oracle only when the model has no verdict. Fail closed when neither decides.
func combineLLMAuthoritative(oracleResult oracleOutcome, judgeResult judgeOutcome) Verdict {
	if judgeResult.err == nil &&
		(judgeResult.result.Verdict == Block || judgeResult.result.Verdict == Allow) {
		return judgeResult.result.Verdict
	}
	if oracleResult.verdict == Block || oracleResult.verdict == Allow {
		return oracleResult.verdict
	}
	return Block
}

func (r *Runtime) logIfNeeded(
	ruleSetID string,
	command string,
	cwd string,
	oracleResult oracleOutcome,
	judgeResult judgeOutcome,
	enforced Verdict,
) {
	reason, shouldLog := disagreementReason(oracleResult, judgeResult)
	if !shouldLog {
		return
	}
	record := DisagreementRecord{
		Timestamp:       r.now().Format(time.RFC3339Nano),
		RuleSetID:       ruleSetID,
		Command:         command,
		CWD:             cwd,
		OracleVerdict:   oracleResult.verdict.String(),
		LLMVerdict:      judgeResult.result.Verdict.String(),
		EnforcedVerdict: enforced.String(),
		Reason:          reason,
		OracleReason:    oracleResult.reason,
		LLMReason:       judgeResult.result.Reason,
		LLMError:        errorString(judgeResult.err),
		OracleLatencyMS: oracleResult.latencyMS,
		LLMLatencyMS:    judgeResult.latencyMS,
	}
	_ = AppendDisagreement(r.disagreementLogPath, record)
}

func disagreementReason(oracleResult oracleOutcome, judgeResult judgeOutcome) (string, bool) {
	if oracleResult.verdict == Unknown {
		if judgeResult.err != nil {
			return "oracle_unknown_llm_error", true
		}
		return "oracle_unknown", true
	}
	if judgeResult.err != nil {
		return "", false
	}
	if oracleResult.verdict != judgeResult.result.Verdict {
		return "oracle_llm_disagreement", true
	}
	return "", false
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func durationMillis(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func defaultOracle(ruleSetID string, command string, cwd string, deps Deps) (Verdict, string) {
	switch ruleSet(ruleSetID) {
	case ruleSetSearchGuard:
		if deps.IndexedRoots == nil {
			return Unknown, "indexed_roots_unavailable"
		}
		ctx, cancel := context.WithTimeout(context.Background(), contextProviderTimeout)
		defer cancel()
		roots, err := deps.IndexedRoots(ctx)
		if err != nil || len(roots) == 0 {
			return Unknown, "indexed_roots_unavailable"
		}
		return oracle.Search(command, cwd, roots), "search_oracle"
	case ruleSetWorktreeGuard:
		if deps.WorktreeState == nil {
			return Unknown, "worktree_state_unavailable"
		}
		state, err := deps.WorktreeState(cwd)
		if err != nil {
			return Unknown, "worktree_state_unavailable"
		}
		return oracle.Worktree(command, cwd, state), "worktree_oracle"
	default:
		return Unknown, "unknown_rule_set"
	}
}

type ruleSet string

const (
	ruleSetSearchGuard   ruleSet = "search-guard"
	ruleSetWorktreeGuard ruleSet = "worktree-guard"
)

func (r *Runtime) defaultJudge(
	ruleSetID string,
	command string,
	cwd string,
	deps Deps,
	requiredContext []string,
) (JudgeResult, error) {
	if !r.judgeEnabled {
		return JudgeResult{Verdict: Unknown, Reason: ""}, errors.New("judge disabled")
	}
	if r.judgeClient == nil {
		return JudgeResult{Verdict: Unknown, Reason: ""}, errors.New("judge client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
	defer cancel()
	reply, err := r.judgeClient.Evaluate(ctx, &judgepb.JudgeRequest{
		InputText: command,
		Context:   Resolve(requiredContext, cwd, deps),
		RuleSetId: ruleSetID,
	})
	if err != nil {
		slog.Warn("evaluate judge rule set failed", "rule_set_id", ruleSetID, "err", err)
		return JudgeResult{Verdict: Unknown, Reason: ""}, fmt.Errorf("evaluate judge rule set %q: %w", ruleSetID, err)
	}
	return judgeReplyResult(reply)
}

func judgeReplyResult(reply *judgepb.JudgeReply) (JudgeResult, error) {
	if reply == nil {
		return JudgeResult{Verdict: Unknown, Reason: ""}, errors.New("nil judge reply")
	}
	switch reply.GetVerdict() {
	case judgepb.Verdict_VERDICT_BLOCK:
		return JudgeResult{Verdict: Block, Reason: reply.GetReason()}, nil
	case judgepb.Verdict_VERDICT_ALLOW:
		return JudgeResult{Verdict: Allow, Reason: reply.GetReason()}, nil
	case judgepb.Verdict_VERDICT_UNSPECIFIED:
		return JudgeResult{Verdict: Unknown, Reason: reply.GetReason()},
			fmt.Errorf("judge returned %s", reply.GetVerdict().String())
	default:
		return JudgeResult{Verdict: Unknown, Reason: reply.GetReason()},
			fmt.Errorf("judge returned %s", reply.GetVerdict().String())
	}
}

func (r *Runtime) requiredContext(ruleSetID string) []string {
	r.ruleSetMu.Lock()
	defer r.ruleSetMu.Unlock()
	if r.ruleSetCache == nil {
		ruleSets, err := r.ruleSetLister()
		if err != nil {
			r.ruleSetCache = map[string][]string{}
		} else {
			r.ruleSetCache = cloneRuleSetContext(ruleSets)
		}
	}
	return append([]string(nil), r.ruleSetCache[ruleSetID]...)
}

func cloneRuleSetContext(ruleSets map[string][]string) map[string][]string {
	out := make(map[string][]string, len(ruleSets))
	for id, requiredContext := range ruleSets {
		out[id] = append([]string(nil), requiredContext...)
	}
	return out
}

func (r *Runtime) defaultRuleSetLister() (map[string][]string, error) {
	if !r.judgeEnabled || r.judgeClient == nil {
		return map[string][]string{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), judgeTimeout)
	defer cancel()
	reply, err := r.judgeClient.ListRuleSets(ctx, &judgepb.ListRuleSetsRequest{})
	if err != nil {
		slog.Warn("list judge rule sets failed", "err", err)
		return nil, fmt.Errorf("list judge rule sets: %w", err)
	}
	out := make(map[string][]string, len(reply.GetRuleSets()))
	for _, descriptor := range reply.GetRuleSets() {
		if descriptor == nil {
			continue
		}
		out[descriptor.GetId()] = append([]string(nil), descriptor.GetRequiredContext()...)
	}
	return out, nil
}
