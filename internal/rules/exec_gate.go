package rules

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/canonpath"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
	"goodkind.io/gksyntax/shelldecomp"
)

// canonCacheTTL bounds how long a path-to-realpath mapping is memoized. Real
// paths rarely change, and the same working directories repeat heavily across
// events, so a short window removes redundant lstat syscalls without holding a
// stale result long enough to matter.
const canonCacheTTL = 5 * time.Second

// backgroundValidatorTimeout bounds a validator run that outlived its event's
// synchronous budget. The run continues detached so its verdict can land in
// the cache and decide the next event for the same target; 30s rides out a
// busy lm-semantic-search daemon while still reclaiming the process.
const backgroundValidatorTimeout = 30 * time.Second

// ExecRuntime holds the cross-event state for exec validator conditions: the
// process runner, a path canonicalization cache, and the result cache keyed by
// canonical cache key. The cache is stale-while-revalidate: a cold key forks
// synchronously (the first event blocks on the real verdict), and once an entry
// exists it is served immediately on every event, with a single background
// refresh kicked off when the entry is older than the rule's cache_ttl_ms. The
// daemon builds one ExecRuntime per config snapshot and discards it on reload,
// which is how the cache resets when config changes. It is safe for concurrent
// use across events.
type ExecRuntime struct {
	runner execconcern.Runner
	canon  *canonpath.Cache
	log    *slog.Logger
	now    func() time.Time

	mu         sync.Mutex
	cache      map[string]cachedVerdict
	refreshing map[string]struct{}
}

type cachedVerdict struct {
	verdict  execconcern.Verdict
	storedAt time.Time
}

// cacheState reports how a cache lookup resolved against the rule's TTL.
type cacheState int

const (
	cacheMiss  cacheState = iota // no entry: the caller must fork synchronously.
	cacheFresh                   // entry younger than the TTL: serve as-is.
	cacheStale                   // entry past the TTL: serve, then refresh async.
)

// NewExecRuntime returns an ExecRuntime using runner to fork validators and
// logging errors to log. A nil runner falls back to the production OS runner and
// a nil log falls back to the default logger.
func NewExecRuntime(runner execconcern.Runner, log *slog.Logger) *ExecRuntime {
	if runner == nil {
		runner = execconcern.OSRunner{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &ExecRuntime{
		runner:     runner,
		canon:      canonpath.NewCache(canonCacheTTL),
		log:        log,
		now:        time.Now,
		mu:         sync.Mutex{},
		cache:      make(map[string]cachedVerdict),
		refreshing: make(map[string]struct{}),
	}
}

var (
	defaultExecRuntimeOnce sync.Once
	defaultExecRuntimeInst *ExecRuntime
)

// defaultExecRuntime returns the process-wide runtime used when no per-snapshot
// runtime is supplied (for example by non-daemon callers of EvaluateAll). It
// uses the real OS runner.
func defaultExecRuntime() *ExecRuntime {
	defaultExecRuntimeOnce.Do(func() {
		defaultExecRuntimeInst = NewExecRuntime(nil, nil)
	})
	return defaultExecRuntimeInst
}

type execRuntimeKey struct{}

type execEventMemoKey struct{}

// WithExecRuntime returns a context carrying runtime so exec conditions
// evaluated under it reuse the same caches and runner.
func WithExecRuntime(ctx context.Context, runtime *ExecRuntime) context.Context {
	return context.WithValue(ctx, execRuntimeKey{}, runtime)
}

func execRuntimeFromContext(ctx context.Context) *ExecRuntime {
	if ctx == nil {
		return nil
	}
	runtime, _ := ctx.Value(execRuntimeKey{}).(*ExecRuntime)
	return runtime
}

// execEventMemo guarantees one validator fork per event per condition and
// carries the per-rule message overrides emitted by blocking validators. It is
// created once per EvaluateAll call. Conditions run sequentially within one
// event (single scheduler slot), but the mutex keeps it safe regardless.
type execEventMemo struct {
	system    string
	eventName string

	mu        sync.Mutex
	verdicts  map[*config.Condition]execconcern.Verdict
	overrides map[string]string
}

func newExecEventMemo(system string, eventName string) *execEventMemo {
	return &execEventMemo{
		system:    system,
		eventName: eventName,
		mu:        sync.Mutex{},
		verdicts:  make(map[*config.Condition]execconcern.Verdict),
		overrides: make(map[string]string),
	}
}

func (m *execEventMemo) lookup(c *config.Condition) (execconcern.Verdict, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.verdicts[c]
	return v, ok
}

func (m *execEventMemo) record(c *config.Condition, ruleName string, v execconcern.Verdict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verdicts[c] = v
	if v.Block && v.Message != "" {
		m.overrides[ruleName] = v.Message
	}
}

func (m *execEventMemo) overrideFor(ruleName string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, ok := m.overrides[ruleName]
	return msg, ok
}

func withExecEventMemo(ctx context.Context, memo *execEventMemo) context.Context {
	return context.WithValue(ctx, execEventMemoKey{}, memo)
}

func execEventMemoFromContext(ctx context.Context) *execEventMemo {
	if ctx == nil {
		return nil
	}
	memo, _ := ctx.Value(execEventMemoKey{}).(*execEventMemo)
	return memo
}

// execConditionGateMatch runs the exec validator for one condition and reports
// whether it blocks. The result is memoized per event so the command forks at
// most once per event, and cached across events by canonical cache key so a hot
// working set forks rarely. Error outcomes are never cached and are logged.
func execConditionGateMatch(ctx context.Context, fields FieldSet, rule *config.Rule, c *config.Condition) bool {
	memo := execEventMemoFromContext(ctx)
	if memo != nil {
		if v, ok := memo.lookup(c); ok {
			return v.Block
		}
	}

	runtime := execRuntimeFromContext(ctx)
	if runtime == nil {
		runtime = defaultExecRuntime()
	}

	cwd := fields.BaseCWD()
	var keyValue string
	if c.CacheKeySelector().Selector == config.FieldCmdReadTargets {
		// cmd_read_targets is rule policy: the tool set comes from this
		// condition's search_tools, which the generic selector path cannot see.
		keyValue = fields.CmdReadTargets(c.SearchTools)
	} else {
		keyValue = fields.String(c.CacheKeySelector().Selector)
	}
	keyView := canonicalizePathField(runtime.canon, cwd, keyValue)
	cacheKey := keyView.Canonical

	if c.CacheTTLMs > 0 {
		v, state := runtime.cacheLookup(c, cacheKey)
		switch state {
		case cacheFresh:
			if memo != nil {
				memo.record(c, rule.Name, v)
			}
			return v.Block
		case cacheStale:
			// Serve the cached verdict now and refresh in the background so no
			// event after the cold one ever blocks on the fork.
			runtime.triggerRefresh(ctx, fields, rule, c, keyView, cacheKey)
			if memo != nil {
				memo.record(c, rule.Name, v)
			}
			return v.Block
		case cacheMiss:
			// Cold key: fall through to a synchronous fork.
		}
	}

	verdict := runtime.runValidator(ctx, fields, rule, c, keyView, memo)

	if !verdict.Errored && c.CacheTTLMs > 0 {
		runtime.cacheStore(c, cacheKey, verdict)
	}
	if memo != nil {
		memo.record(c, rule.Name, verdict)
	}
	return verdict.Block
}

// triggerRefresh forks the validator off the hot path to refresh a stale cache
// entry, deduped so at most one refresh per key is in flight. The refresh is
// detached from the request cancellation (the event already has its verdict)
// and never caches an error outcome, so a transient failure keeps the last good
// verdict instead of clearing it.
func (r *ExecRuntime) triggerRefresh(ctx context.Context, fields FieldSet, rule *config.Rule, c *config.Condition, keyView execconcern.PathView, cacheKey string) {
	key := cacheEntryKey(c, cacheKey)
	r.mu.Lock()
	if _, busy := r.refreshing[key]; busy {
		r.mu.Unlock()
		return
	}
	r.refreshing[key] = struct{}{}
	r.mu.Unlock()

	refreshCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			r.mu.Lock()
			delete(r.refreshing, key)
			r.mu.Unlock()
			if rec := recover(); rec != nil {
				r.log.ErrorContext(refreshCtx, "exec validator refresh panic", "rule", rule.Name, "err", fmt.Errorf("panic: %v", rec))
			}
		}()
		verdict := r.runValidator(refreshCtx, fields, rule, c, keyView, nil)
		if !verdict.Errored {
			r.cacheStore(c, cacheKey, verdict)
		}
	}()
}

func (r *ExecRuntime) runValidator(
	ctx context.Context,
	fields FieldSet,
	rule *config.Rule,
	c *config.Condition,
	keyView execconcern.PathView,
	memo *execEventMemo,
) execconcern.Verdict {
	in := r.buildInput(fields, rule, c, keyView, memo)
	stdin, env, err := execconcern.BuildRequest(in)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator request build failed",
			"rule", rule.Name, "on_error", c.OnError, "err", err)
		return execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Errored: true}
	}

	done := make(chan execconcern.Verdict, 1)
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.ErrorContext(bgCtx, "exec validator panic",
					"rule", rule.Name, "err", fmt.Errorf("panic: %v", rec))
				done <- execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Errored: true}
			}
		}()
		res, runErr := r.runner.Run(bgCtx, c.Command, backgroundValidatorTimeout, stdin, env)
		verdict := execconcern.Interpret(c, res, runErr)
		if verdict.Errored {
			r.log.WarnContext(bgCtx, "exec validator errored",
				"rule", rule.Name, "on_error", c.OnError, "block", verdict.Block, "err", runErr)
		}
		done <- verdict
	}()

	syncBudget := time.Duration(c.TimeoutMs) * time.Millisecond
	if syncBudget <= 0 {
		return <-done
	}
	timer := time.NewTimer(syncBudget)
	defer timer.Stop()
	select {
	case verdict := <-done:
		return verdict
	case <-timer.C:
		// The event fails open now, but the run continues so its verdict can
		// decide the next event for this target. Registration in refreshing
		// keeps a stale-entry refresh from forking a duplicate meanwhile.
		key := cacheEntryKey(c, keyView.Canonical)
		r.mu.Lock()
		_, busy := r.refreshing[key]
		if !busy {
			r.refreshing[key] = struct{}{}
		}
		r.mu.Unlock()
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					r.log.ErrorContext(bgCtx, "exec validator background cache panic",
						"rule", rule.Name, "err", fmt.Errorf("panic: %v", rec))
				}
			}()
			verdict := <-done
			if !busy {
				r.mu.Lock()
				delete(r.refreshing, key)
				r.mu.Unlock()
			}
			if !verdict.Errored && c.CacheTTLMs > 0 {
				r.cacheStore(c, keyView.Canonical, verdict)
			}
		}()
		r.log.WarnContext(ctx, "exec validator exceeded synchronous budget; continuing in background",
			"rule", rule.Name, "on_error", c.OnError, "budget_ms", c.TimeoutMs)
		return execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Errored: true}
	}
}

func (r *ExecRuntime) buildInput(
	fields FieldSet,
	rule *config.Rule,
	c *config.Condition,
	keyView execconcern.PathView,
	memo *execEventMemo,
) execconcern.Input {
	cwd := fields.BaseCWD()
	system := ""
	eventName := ""
	if memo != nil {
		system = memo.system
		eventName = memo.eventName
	}
	command := fields.String(config.FieldToolInputCommand)
	if command == "" {
		command = fields.String(config.FieldCommand)
	}
	matched := make([]execconcern.FieldValue, 0, len(c.Selectors()))
	for _, sel := range c.Selectors() {
		value := fields.String(sel.Selector)
		if value == "" {
			continue
		}
		matched = append(matched, execconcern.FieldValue{Field: sel.Path, Value: value})
	}
	effectiveCwd := fields.String(config.FieldEffectiveCWD)
	if effectiveCwd == shelldecomp.Unresolvable {
		// The marker means "directory unknown". The validator contract
		// expresses that as an empty cwd, and the marker's NUL byte must
		// never reach the process environment.
		effectiveCwd = ""
	}
	return execconcern.Input{
		Event:        eventName,
		System:       system,
		ToolName:     fields.String(config.FieldToolName),
		Rule:         rule.Name,
		Command:      command,
		EffectiveCWD: canonicalizePathField(r.canon, cwd, effectiveCwd),
		FilePath:     canonicalizePathField(r.canon, cwd, fields.FilePathValue()),
		CacheKey:     keyView,
		ReadTargets:  r.readTargetViews(cwd, command, c.SearchTools),
		Matched:      matched,
	}
}

// readTargetViews canonicalizes the effective filesystem targets of a
// code-search command so the validator can check each path's index status
// rather than the working directory, scoped to the condition's declared
// search_tools. The base (pre-cd) working directory is passed to
// ExtractCodeSearchTargets, which decomposes the whole command with shelldecomp
// and applies the cd chain itself, so a search run after `cd /other` is
// attributed to /other rather than the session cwd without applying the cd
// chain twice.
func (r *ExecRuntime) readTargetViews(cwd, command string, searchTools []string) []execconcern.PathView {
	targets := shellread.ExtractCodeSearchTargets(command, cwd, searchTools)
	views := make([]execconcern.PathView, 0, len(targets))
	for _, target := range targets {
		if target.Remote || target.Path == "" {
			continue
		}
		views = append(views, canonicalizePathField(r.canon, cwd, target.Path))
	}
	return views
}

// cacheLookup reports the cached verdict and whether it is missing, fresh, or
// stale relative to the condition's cache_ttl_ms.
func (r *ExecRuntime) cacheLookup(c *config.Condition, cacheKey string) (execconcern.Verdict, cacheState) {
	key := cacheEntryKey(c, cacheKey)
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.cache[key]
	if !ok {
		return execconcern.Verdict{Block: false, Message: "", Errored: false}, cacheMiss
	}
	if now.Sub(entry.storedAt) < time.Duration(c.CacheTTLMs)*time.Millisecond {
		return entry.verdict, cacheFresh
	}
	return entry.verdict, cacheStale
}

func (r *ExecRuntime) cacheStore(c *config.Condition, cacheKey string, verdict execconcern.Verdict) {
	key := cacheEntryKey(c, cacheKey)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[key] = cachedVerdict{verdict: verdict, storedAt: r.now()}
}

func cacheEntryKey(c *config.Condition, cacheKey string) string {
	return fmt.Sprintf("%p\x00%s", c, cacheKey)
}

// canonicalizePathField resolves a path-like field value to its canonical real
// path. A non-path value (no separators, not absolute) is used verbatim as the
// key with IsCanonical false, so a non-path cache key still works as a literal.
func canonicalizePathField(cache *canonpath.Cache, cwd string, value string) execconcern.PathView {
	if value == "" {
		return execconcern.PathView{Raw: "", Canonical: "", IsCanonical: false}
	}
	if !looksLikePath(value) {
		return execconcern.PathView{Raw: value, Canonical: value, IsCanonical: false}
	}
	result := cache.Resolve(cwd, value)
	return execconcern.PathView{
		Raw:         result.Raw,
		Canonical:   result.Canonical,
		IsCanonical: result.IsCanonical,
	}
}

func looksLikePath(value string) bool {
	return filepath.IsAbs(value) ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		strings.Contains(value, "/")
}
