package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules/canonpath"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func (r *ExecRuntime) cacheLookup(
	ctx context.Context,
	cacheNamespace string,
	cacheKey string,
) (execconcern.Verdict, bool) {
	if r.cache == nil {
		return execconcern.Verdict{
			Block:        false,
			Message:      "",
			Output:       "",
			Errored:      false,
			PartialError: false,
		}, false
	}

	entry, found, err := r.cache.Get(cacheNamespace, cacheKey)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache get failed", "err", err)

		return execconcern.Verdict{
			Block:        false,
			Message:      "",
			Output:       "",
			Errored:      false,
			PartialError: false,
		}, false
	}

	if !found {
		return execconcern.Verdict{
			Block:        false,
			Message:      "",
			Output:       "",
			Errored:      false,
			PartialError: false,
		}, false
	}

	var cached cachedExecVerdict
	if err := json.Unmarshal(entry.Value, &cached); err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache decode failed", "err", err)
		_, _ = r.cache.Delete(cacheNamespace, cacheKey)

		return execconcern.Verdict{
			Block:        false,
			Message:      "",
			Output:       "",
			Errored:      false,
			PartialError: false,
		}, false
	}

	return execconcern.Verdict{
		Block:   cached.Block,
		Message: cached.Message,
		Output:  cached.Output,
		Errored: false,
		// Only a clean verdict is cached, and the cached entry says nothing
		// about the expansion that produced it, so a cache hit never claims a
		// partial failure it cannot substantiate.
		PartialError: false,
	}, true
}

func (r *ExecRuntime) cacheStore(
	ctx context.Context,
	cacheNamespace string,
	cacheKey string,
	ttlMs int,
	verdict execconcern.Verdict,
) {
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

	_, _, err = r.cache.Set(
		cacheNamespace,
		cacheKey,
		value,
		hotkv.SetOptions{
			Mode: hotkv.SetModeAny,
			TTL:  time.Duration(ttlMs) * time.Millisecond,
		},
	)
	if err != nil {
		r.log.WarnContext(ctx, "exec validator hot cache set failed", "err", err)
	}
}

func stableExecCacheEntryKey(
	rule *config.Rule,
	conditionIndex int,
	condition *config.Condition,
	keyView execconcern.PathView,
	cacheScope execTemporalCacheScope,
) string {
	hash := sha256.New()
	writeHashPart := func(value string) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	writeHashPart(rule.Name)
	writeHashPart(rule.Action)
	writeHashPart(rule.OutputText())
	writeHashPart(strconv.Itoa(conditionIndex))
	writeHashPart(condition.Kind)
	writeHashPart(strings.Join(condition.Command, "\x1f"))
	writeHashPart(condition.CacheKey)
	writeHashPart(condition.ForEach)
	writeHashPart(condition.MatchMode)

	for _, selector := range condition.Selectors() {
		writeHashPart(selector.Path)
	}

	writeHashPart(condition.BlockOn)
	writeHashPart(condition.OnError)
	writeHashPart(condition.StdoutJSONField)
	writeHashPart(string(condition.StdoutJSONEqualsValue().Kind()))
	writeHashPart(condition.StdoutJSONEqualsValue().CanonicalString())
	writeHashPart(strconv.Itoa(condition.CacheTTLMs))
	writeHashPart(strconv.Itoa(condition.TimeoutMs))
	writeHashPart(strconv.Itoa(condition.RetryCount))
	writeHashPart(strings.Join(condition.SearchTools, "\x1f"))

	for _, spec := range condition.WriteSpecs {
		writeHashPart(strings.Join(spec.Argv0, "\x1f"))
		writeHashPart(spec.TargetMode)
		writeHashPart(strings.Join(spec.SkipFlagsWithValues, "\x1f"))
		writeHashPart(strconv.FormatBool(spec.EndOfOptions))
		writeHashPart(strings.Join(spec.CwdFlags, "\x1f"))
	}

	if execConditionUsesTemporalSelector(condition) {
		writeHashPart(cacheScope.system)
		writeHashPart(cacheScope.conversationID)
		writeHashPart(cacheScope.eventName)
		writeHashPart(cacheScope.responseTarget)
	}

	writeHashPart(keyView.Canonical)

	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func canonicalizeCacheKeyField(
	cache *canonpath.Cache,
	cwd string,
	value string,
) execconcern.PathView {
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
