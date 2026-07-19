package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
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

// maxResolvedScriptBytes caps the size of an interpreter script the disk
// resolver reads off disk for code-search read analysis. A program file larger
// than this is refused so a pathological input cannot pull an unbounded read
// into the parse; real agent scripts are far smaller than 1 MiB.
const maxResolvedScriptBytes = 1 << 20

const execValidatorCacheNamespace = "exec-validator"

// ExecRuntime holds the cross-event state for exec validator conditions: the
// process runner, a path canonicalization cache, a daemon hot cache, and
// singleflight state for cold cache misses. The hot cache is process-local and
// intentionally non-durable; daemon snapshots can share it across config reloads
// so stable rules keep their short TTL debounce window.
type ExecRuntime struct {
	runner execconcern.Runner
	canon  *canonpath.Cache
	cache  *hotkv.Store
	log    *slog.Logger

	mu       sync.Mutex
	inflight map[string]*validatorFlight
}

type validatorFlight struct {
	done    chan struct{}
	verdict execconcern.Verdict
}

type cachedExecVerdict struct {
	Block   bool   `json:"block"`
	Message string `json:"message"`
	Output  string `json:"output"`
}

type validatorRunResult struct {
	verdict    execconcern.Verdict
	background <-chan execconcern.Verdict
}

// NewExecRuntime returns an ExecRuntime using runner to fork validators and
// logging errors to log. A nil runner falls back to the production OS runner and
// a nil log falls back to the default logger.
func NewExecRuntime(runner execconcern.Runner, log *slog.Logger) *ExecRuntime {
	return NewExecRuntimeWithCache(runner, log, nil)
}

// NewExecRuntimeWithCache returns an ExecRuntime backed by cache. A nil cache
// gets a private in-memory store with no periodic prune goroutine, which keeps
// tests and standalone callers isolated.
func NewExecRuntimeWithCache(runner execconcern.Runner, log *slog.Logger, cache *hotkv.Store) *ExecRuntime {
	if runner == nil {
		runner = execconcern.OSRunner{}
	}
	if log == nil {
		log = slog.Default()
	}
	if cache == nil {
		cache = hotkv.New(hotkv.Options{
			MaxEntries:    hotkv.DefaultMaxEntries,
			MaxValueBytes: hotkv.DefaultMaxValueBytes,
			PruneInterval: 0,
		})
	}
	return &ExecRuntime{
		runner:   runner,
		canon:    canonpath.NewCache(canonCacheTTL),
		cache:    cache,
		log:      log,
		mu:       sync.Mutex{},
		inflight: make(map[string]*validatorFlight),
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
func execConditionGateMatch(ctx context.Context, fields FieldSet, rule *config.Rule, conditionIndex int, c *config.Condition) bool {
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
	keyValue := execCacheKeyValue(fields, c)
	keyView := canonicalizeCacheKeyField(runtime.canon, cwd, keyValue)
	cacheKey := stableExecCacheEntryKey(rule, conditionIndex, c, keyView)

	if c.CacheTTLMs > 0 {
		if v, ok := runtime.cacheLookup(ctx, cacheKey); ok {
			if memo != nil {
				memo.record(c, rule.Name, v)
			}
			return v.Block
		}
	}

	verdict := runtime.runValidatorSingleflight(ctx, fields, rule, c, keyView, cacheKey, memo)

	if memo != nil {
		memo.record(c, rule.Name, verdict)
	}
	return verdict.Block
}

func execCacheKeyValue(fields FieldSet, c *config.Condition) string {
	selector := c.CacheKeySelector().Selector
	return execSelectorValue(fields, selector, c)
}

func execSelectorValue(fields FieldSet, selector config.FieldSelector, c *config.Condition) string {
	if selector == config.FieldCmdReadTargets {
		return fields.CmdReadTargets(c.SearchTools, diskFileResolver())
	}
	if selector == config.FieldExecTargets {
		return fields.ExecTargets(c.SearchTools, diskFileResolver())
	}
	return fields.StringForCondition(selector, c)
}

func (r *ExecRuntime) expandExecCommands(fields FieldSet, c *config.Condition) [][]string {
	if c.ForEachSelector().Selector == config.FieldSelectorInvalid {
		command := make([]string, len(c.Command))
		copy(command, c.Command)
		return [][]string{command}
	}

	items := r.forEachItems(fields, c)
	if len(items) == 0 {
		return nil
	}

	commands := make([][]string, 0, len(items))
	for _, item := range items {
		command := make([]string, len(c.Command))
		for i, arg := range c.Command {
			command[i] = strings.ReplaceAll(arg, "{{item}}", item)
		}
		commands = append(commands, command)
	}
	return commands
}

func (r *ExecRuntime) forEachItems(fields FieldSet, c *config.Condition) []string {
	raw := execSelectorValue(fields, c.ForEachSelector().Selector, c)
	if raw == "" {
		return nil
	}

	cwd := fields.BaseCWD()
	seen := make(map[string]struct{})
	items := make([]string, 0, strings.Count(raw, "\n")+1)
	for part := range strings.SplitSeq(raw, "\n") {
		if part == "" {
			continue
		}
		view := canonicalizePathField(r.canon, cwd, part)
		value := view.Canonical
		if value == "" {
			value = view.Raw
		}
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		items = append(items, value)
	}
	return items
}

func (r *ExecRuntime) runValidatorSingleflight(
	ctx context.Context,
	fields FieldSet,
	rule *config.Rule,
	c *config.Condition,
	keyView execconcern.PathView,
	cacheKey string,
	memo *execEventMemo,
) execconcern.Verdict {
	if c.CacheTTLMs <= 0 {
		return r.runValidator(ctx, fields, rule, c, keyView, memo).verdict
	}

	r.mu.Lock()
	flight, ok := r.inflight[cacheKey]
	if ok {
		done := flight.done
		r.mu.Unlock()
		select {
		case <-done:
			return flight.verdict
		case <-ctx.Done():
			return execconcern.Verdict{
				Block:   c.OnError == config.OnErrorClosed,
				Message: "",
				Output:  "",
				Errored: true,
			}
		}
	}
	flight = &validatorFlight{
		done:    make(chan struct{}),
		verdict: execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false},
	}
	r.inflight[cacheKey] = flight
	r.mu.Unlock()

	verdict := execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
	backgroundManaged := false
	defer func() {
		if recovered := recover(); recovered != nil {
			r.log.ErrorContext(ctx, "exec validator singleflight panic",
				"rule", rule.Name, "err", fmt.Errorf("panic: %v", recovered))
			verdict = execconcern.Verdict{
				Block:   c.OnError == config.OnErrorClosed,
				Message: "",
				Output:  "",
				Errored: true,
			}
		}
		if backgroundManaged {
			return
		}
		if !verdict.Errored && c.CacheTTLMs > 0 {
			r.cacheStore(ctx, cacheKey, c.CacheTTLMs, verdict)
		}
		r.finishValidatorFlight(cacheKey, flight, verdict)
	}()

	runResult := r.runValidator(ctx, fields, rule, c, keyView, memo)
	verdict = runResult.verdict
	if runResult.background != nil {
		backgroundManaged = true
		go func() {
			backgroundCtx := context.WithoutCancel(ctx)
			defer func() {
				if recovered := recover(); recovered != nil {
					r.log.ErrorContext(backgroundCtx, "exec validator background completion panic",
						"rule", rule.Name, "err", fmt.Errorf("panic: %v", recovered))
					r.finishValidatorFlight(cacheKey, flight, execconcern.Verdict{
						Block:   c.OnError == config.OnErrorClosed,
						Message: "",
						Output:  "",
						Errored: true,
					})
				}
			}()
			backgroundVerdict := <-runResult.background
			if !backgroundVerdict.Errored && c.CacheTTLMs > 0 {
				r.cacheStore(backgroundCtx, cacheKey, c.CacheTTLMs, backgroundVerdict)
			}
			r.finishValidatorFlight(cacheKey, flight, backgroundVerdict)
		}()
	}

	return verdict
}

func (r *ExecRuntime) finishValidatorFlight(cacheKey string, flight *validatorFlight, verdict execconcern.Verdict) {
	if flight == nil {
		return
	}
	flight.verdict = verdict
	close(flight.done)

	r.mu.Lock()
	delete(r.inflight, cacheKey)
	r.mu.Unlock()
}

func (r *ExecRuntime) runValidator(
	ctx context.Context,
	fields FieldSet,
	rule *config.Rule,
	c *config.Condition,
	keyView execconcern.PathView,
	memo *execEventMemo,
) validatorRunResult {
	in := r.buildInput(fields, rule, c, keyView, memo)
	stdin, env, err := execconcern.BuildRequest(in)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator request build failed",
			"rule", rule.Name, "on_error", c.OnError, "err", err)
		return validatorRunResult{
			verdict:    execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Output: "", Errored: true},
			background: nil,
		}
	}
	commands := r.expandExecCommands(fields, c)
	if len(commands) == 0 {
		return validatorRunResult{
			verdict:    execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false},
			background: nil,
		}
	}

	done := make(chan execconcern.Verdict, 1)
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				r.log.ErrorContext(bgCtx, "exec validator panic",
					"rule", rule.Name, "err", fmt.Errorf("panic: %v", rec))
				done <- execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Output: "", Errored: true}
			}
		}()
		verdict := r.runExpandedCommands(bgCtx, rule.Name, c, commands, stdin, env)
		if verdict.Errored {
			r.log.WarnContext(bgCtx, "exec validator errored",
				"rule", rule.Name, "on_error", c.OnError, "block", verdict.Block)
		}
		done <- verdict
	}()

	syncBudget := time.Duration(c.TimeoutMs) * time.Millisecond
	if syncBudget <= 0 {
		return validatorRunResult{verdict: <-done, background: nil}
	}
	timer := time.NewTimer(syncBudget)
	defer timer.Stop()
	select {
	case verdict := <-done:
		return validatorRunResult{verdict: verdict, background: nil}
	case <-timer.C:
		r.log.WarnContext(ctx, "exec validator exceeded synchronous budget; continuing in background",
			"rule", rule.Name, "on_error", c.OnError, "budget_ms", c.TimeoutMs)
		return validatorRunResult{
			verdict:    execconcern.Verdict{Block: c.OnError == config.OnErrorClosed, Message: "", Output: "", Errored: true},
			background: done,
		}
	}
}

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
	for _, command := range commands {
		res, runErr := r.runner.Run(ctx, command, backgroundValidatorTimeout, stdin, env)
		verdict := execconcern.Interpret(c, res, runErr)
		if verdict.Errored {
			r.logExpandedCommandError(ctx, ruleName, c, command, res, runErr)
			return verdict
		}
		if !forEach {
			return verdict
		}
		if verdict.Block && firstBlockMessage == "" {
			firstBlockMessage = verdict.Message
		}
		if !matchAll && verdict.Block {
			return verdict
		}
		if matchAll && !verdict.Block {
			return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
		}
	}
	if matchAll {
		return execconcern.Verdict{Block: true, Message: firstBlockMessage, Output: "", Errored: false}
	}
	return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}
}

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
			"rule", ruleName, "on_error", c.OnError, "command", command, "stdout_first_line", firstStdoutLine(res.Stdout))
	default:
		r.log.WarnContext(ctx, "exec validator expanded command produced an errored verdict",
			"rule", ruleName, "on_error", c.OnError, "command", command, "exit_code", res.ExitCode)
	}
}

func firstStdoutLine(stdout string) string {
	trimmed := strings.TrimLeft(stdout, "\r\n")
	if index := strings.IndexAny(trimmed, "\r\n"); index >= 0 {
		return strings.TrimSpace(trimmed[:index])
	}
	return strings.TrimSpace(trimmed)
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
		value := fields.StringForCondition(sel.Selector, c)
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
	targets := shellread.ExtractCodeSearchTargets(command, cwd, searchTools, diskFileResolver())
	views := make([]execconcern.PathView, 0, len(targets))
	for _, target := range targets {
		if target.Remote || target.Path == "" {
			continue
		}
		views = append(views, canonicalizePathField(r.canon, cwd, target.Path))
	}
	return views
}

// diskFileResolver returns a shelldecomp.FileResolver that reads an interpreter
// script off disk only when the path names an existing regular file under the
// size cap. A missing path, a directory or other non-regular file, a file over
// the cap, or any read error yields (nil, false), so an unreadable or oversized
// script is located but never parsed. The resolver receives an absolute path
// that shelldecomp has already resolved against the command cwd.
func diskFileResolver() shelldecomp.FileResolver {
	return func(absPath string) ([]byte, bool) {
		info, err := os.Stat(absPath)
		if err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		if info.Size() > maxResolvedScriptBytes {
			return nil, false
		}
		content, err := os.ReadFile(absPath)
		if err != nil {
			return nil, false
		}
		return content, true
	}
}

func (r *ExecRuntime) cacheLookup(ctx context.Context, cacheKey string) (execconcern.Verdict, bool) {
	if r.cache == nil {
		return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}, false
	}
	entry, found, err := r.cache.Get(execValidatorCacheNamespace, cacheKey)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache get failed", "err", err)
		return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}, false
	}
	if !found {
		return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}, false
	}

	var cached cachedExecVerdict
	if err := json.Unmarshal(entry.Value, &cached); err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache decode failed", "err", err)
		_, _ = r.cache.Delete(execValidatorCacheNamespace, cacheKey)
		return execconcern.Verdict{Block: false, Message: "", Output: "", Errored: false}, false
	}
	return execconcern.Verdict{
		Block:   cached.Block,
		Message: cached.Message,
		Output:  cached.Output,
		Errored: false,
	}, true
}

func (r *ExecRuntime) cacheStore(ctx context.Context, cacheKey string, ttlMs int, verdict execconcern.Verdict) {
	if r.cache == nil {
		return
	}
	cached := cachedExecVerdict{
		Block:   verdict.Block,
		Message: verdict.Message,
		Output:  verdict.Output,
	}
	value, err := json.Marshal(cached)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache encode failed", "err", err)
		return
	}
	_, _, err = r.cache.Set(execValidatorCacheNamespace, cacheKey, value, hotkv.SetOptions{
		Mode: hotkv.SetModeAny,
		TTL:  time.Duration(ttlMs) * time.Millisecond,
	})
	if err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache set failed", "err", err)
	}
}

func stableExecCacheEntryKey(rule *config.Rule, conditionIndex int, c *config.Condition, keyView execconcern.PathView) string {
	hash := sha256.New()
	writeHashPart := func(value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	writeHashPart(rule.Name)
	writeHashPart(strconv.Itoa(conditionIndex))
	writeHashPart(c.Kind)
	writeHashPart(strings.Join(c.Command, "\x1f"))
	writeHashPart(c.CacheKey)
	writeHashPart(c.ForEach)
	writeHashPart(c.MatchMode)
	for _, selector := range c.Selectors() {
		writeHashPart(selector.Path)
	}
	writeHashPart(c.BlockOn)
	writeHashPart(c.OnError)
	writeHashPart(c.StdoutJSONField)
	writeHashPart(string(c.StdoutJSONEqualsValue().Kind()))
	writeHashPart(c.StdoutJSONEqualsValue().CanonicalString())
	writeHashPart(strconv.Itoa(c.CacheTTLMs))
	writeHashPart(strconv.Itoa(c.TimeoutMs))
	writeHashPart(strings.Join(c.SearchTools, "\x1f"))
	for _, spec := range c.WriteSpecs {
		writeHashPart(strings.Join(spec.Argv0, "\x1f"))
		writeHashPart(spec.TargetMode)
		writeHashPart(strings.Join(spec.SkipFlagsWithValues, "\x1f"))
		writeHashPart(strconv.FormatBool(spec.EndOfOptions))
		writeHashPart(strings.Join(spec.CwdFlags, "\x1f"))
	}
	writeHashPart(keyView.Canonical)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func canonicalizeCacheKeyField(cache *canonpath.Cache, cwd string, value string) execconcern.PathView {
	if !strings.Contains(value, "\n") {
		return canonicalizePathField(cache, cwd, value)
	}

	parts := strings.Split(value, "\n")
	canonicalParts := make([]string, 0, len(parts))
	allCanonical := true
	for _, part := range parts {
		if part == "" {
			continue
		}
		view := canonicalizePathField(cache, cwd, part)
		canonicalParts = append(canonicalParts, view.Canonical)
		if !view.IsCanonical {
			allCanonical = false
		}
	}
	return execconcern.PathView{
		Raw:         value,
		Canonical:   strings.Join(canonicalParts, "\n"),
		IsCanonical: allCanonical,
	}
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
