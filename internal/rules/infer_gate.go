package rules

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/clyde/api/contextpb"
)

const inferenceCacheNamespace = "infer-condition"

// InferenceTrace is the payload-free record of one attempted inference layer.
type InferenceTrace struct {
	LayerName      string        `json:"layer_name"`
	ConditionIndex int           `json:"condition_index"`
	Model          string        `json:"model,omitempty"`
	Endpoint       string        `json:"endpoint"`
	Outcome        string        `json:"outcome"`
	Status         string        `json:"status"`
	Latency        time.Duration `json:"latency"`
	CacheHit       bool          `json:"cache_hit"`
	ErrorClass     string        `json:"error_class,omitempty"`
}

// InferenceTraceCollector receives sanitized in-memory layer traces.
type InferenceTraceCollector interface {
	CollectInferenceTrace(InferenceTrace)
}

type inferenceTraceCollectorKey struct{}

// WithInferenceTraceCollector attaches a collector to an evaluation context.
func WithInferenceTraceCollector(ctx context.Context, collector InferenceTraceCollector) context.Context {
	return context.WithValue(ctx, inferenceTraceCollectorKey{}, collector)
}

type inferFlight struct {
	done   chan struct{}
	result inferResult
}

type inferResult struct {
	matched    bool
	errored    bool
	errorClass string
	cacheHit   bool
}

type cachedInferResult struct {
	Matched bool `json:"matched"`
}

// InferRuntime owns reusable inference and context channels plus call state.
type InferRuntime struct {
	log                  *slog.Logger
	cache                *hotkv.Store
	ownsCache            bool
	mu                   sync.Mutex
	inferenceConnections map[string]*grpc.ClientConn
	inferenceClients     map[string]inferencepb.InferenceClient
	contextConnections   map[string]*grpc.ClientConn
	contextClients       map[string]contextpb.ConversationContextClient
	inflight             map[string]*inferFlight
	now                  func() time.Time
}

// NewInferRuntimeWithCache creates a reusable runtime backed by cache.
func NewInferRuntimeWithCache(log *slog.Logger, cache *hotkv.Store) *InferRuntime {
	if log == nil {
		log = slog.Default()
	}
	ownsCache := false
	if cache == nil {
		cache = hotkv.New(hotkv.Options{
			MaxEntries:    0,
			MaxValueBytes: 0,
			PruneInterval: 0,
		})
		ownsCache = true
	}
	return &InferRuntime{
		log:                  log,
		cache:                cache,
		ownsCache:            ownsCache,
		mu:                   sync.Mutex{},
		inferenceConnections: map[string]*grpc.ClientConn{},
		inferenceClients:     map[string]inferencepb.InferenceClient{},
		contextConnections:   map[string]*grpc.ClientConn{},
		contextClients:       map[string]contextpb.ConversationContextClient{},
		inflight:             map[string]*inferFlight{},
		now:                  time.Now,
	}
}

// Close releases all runtime-owned channels and its private cache.
func (runtime *InferRuntime) Close() {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	connections := make([]*grpc.ClientConn, 0, len(runtime.inferenceConnections)+len(runtime.contextConnections))
	for _, connection := range runtime.inferenceConnections {
		connections = append(connections, connection)
	}
	for _, connection := range runtime.contextConnections {
		connections = append(connections, connection)
	}
	runtime.inferenceConnections = map[string]*grpc.ClientConn{}
	runtime.contextConnections = map[string]*grpc.ClientConn{}
	runtime.inferenceClients = map[string]inferencepb.InferenceClient{}
	runtime.contextClients = map[string]contextpb.ConversationContextClient{}
	runtime.mu.Unlock()
	for _, connection := range connections {
		_ = connection.Close()
	}
	if runtime.ownsCache && runtime.cache != nil {
		runtime.cache.Close()
	}
}

type inferRuntimeKey struct{}

var defaultInferRuntime = NewInferRuntimeWithCache(nil, nil)

// WithInferRuntime attaches daemon-owned reusable inference state.
func WithInferRuntime(ctx context.Context, runtime *InferRuntime) context.Context {
	return context.WithValue(ctx, inferRuntimeKey{}, runtime)
}

func inferRuntimeFromContext(ctx context.Context) *InferRuntime {
	if runtime, ok := ctx.Value(inferRuntimeKey{}).(*InferRuntime); ok && runtime != nil {
		return runtime
	}
	return defaultInferRuntime
}

type inferEventMemo struct {
	mu      sync.Mutex
	results map[*config.Condition]bool
}

type inferEventMemoKey struct{}

func withInferEventMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, inferEventMemoKey{}, &inferEventMemo{
		mu:      sync.Mutex{},
		results: map[*config.Condition]bool{},
	})
}

func inferConditionGateMatch(ctx context.Context, fields FieldSet, rule *config.Rule, conditionIndex int, condition *config.Condition) bool {
	if memo, ok := ctx.Value(inferEventMemoKey{}).(*inferEventMemo); ok {
		memo.mu.Lock()
		result, found := memo.results[condition]
		memo.mu.Unlock()
		if found {
			return result
		}
		result = inferRuntimeFromContext(ctx).evaluate(ctx, fields, rule, conditionIndex, condition)
		memo.mu.Lock()
		memo.results[condition] = result
		memo.mu.Unlock()
		return result
	}
	return inferRuntimeFromContext(ctx).evaluate(ctx, fields, rule, conditionIndex, condition)
}

func (runtime *InferRuntime) evaluate(ctx context.Context, fields FieldSet, rule *config.Rule, conditionIndex int, condition *config.Condition) bool {
	started := runtime.now()
	input := fields.StringForCondition(condition.InputFieldSelector().Selector, condition)
	keyValue := fields.StringForCondition(condition.CacheKeySelector().Selector, condition)
	contextWorkspace, contextSession := resolvedContextIdentity(fields, condition)
	cacheKey := stableInferenceKey(
		condition,
		input,
		keyValue,
		contextWorkspace,
		contextSession,
	)
	result := runtime.resolve(
		ctx,
		condition,
		cacheKey,
		input,
		contextWorkspace,
		contextSession,
	)
	blocked := inferResultBlocks(condition, result)
	runtime.collectTrace(ctx, condition, conditionIndex, result, runtime.now().Sub(started))
	if result.errored {
		runtime.log.WarnContext(ctx, "inference condition failed",
			"endpoint", condition.Endpoint, "rule", rule.Name,
			"condition_index", conditionIndex, "status_class", result.errorClass)
	}
	return blocked
}

func (runtime *InferRuntime) resolve(
	ctx context.Context,
	condition *config.Condition,
	cacheKey string,
	input string,
	contextWorkspace string,
	contextSession string,
) inferResult {
	if result, found := runtime.cacheLookup(cacheKey); found {
		result.cacheHit = true
		return result
	}
	return runtime.singleflight(ctx, cacheKey, func() inferResult {
		if condition.CacheTTLMs > 0 {
			if cached, found := runtime.cacheLookup(cacheKey); found {
				cached.cacheHit = true
				return cached
			}
		}
		result := runtime.call(ctx, condition, input, contextWorkspace, contextSession)
		if !result.errored && condition.CacheTTLMs > 0 {
			runtime.cacheStore(cacheKey, condition.CacheTTLMs, result)
		}
		return result
	})
}

func resolvedContextIdentity(fields FieldSet, condition *config.Condition) (string, string) {
	if condition.ContextSource == "" {
		return "", ""
	}
	workspace := fields.StringForCondition(condition.ContextWorkspaceSelector().Selector, condition)
	session := fields.StringForCondition(condition.ContextSessionSelector().Selector, condition)
	return workspace, session
}

func inferResultBlocks(condition *config.Condition, result inferResult) bool {
	switch {
	case result.errored:
		return condition.OnError == config.OnErrorClosed
	case condition.BlockOn == config.BlockOnMatch:
		return result.matched
	default:
		return !result.matched
	}
}

func (runtime *InferRuntime) call(
	ctx context.Context,
	condition *config.Condition,
	input string,
	contextWorkspace string,
	contextSession string,
) inferResult {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(condition.TimeoutMs)*time.Millisecond)
	defer cancel()
	contextJSON, errClass := runtime.contextJSON(
		callCtx,
		condition,
		contextWorkspace,
		contextSession,
	)
	if errClass != "" && condition.ContextOnError == "error" {
		return inferError(errClass)
	}
	client, err := runtime.inferenceClient(condition.Endpoint)
	if err != nil {
		return inferError("invalid_endpoint")
	}
	reply, err := client.Infer(callCtx, &inferencepb.InferRequest{
		Prompt: condition.Prompt, Input: input,
		OutputSchema: condition.OutputSchema, Context: contextJSON, Model: condition.Model,
	})
	if err != nil {
		return inferError(grpcErrorClass(err))
	}
	if reply == nil || reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
		return inferError("non_complete")
	}
	matched, err := inferenceJSONMatches(condition, reply.GetOutputJson())
	if err != nil {
		return inferError("invalid_response")
	}
	return inferSuccess(matched)
}

func inferError(errorClass string) inferResult {
	return inferResult{matched: false, errored: true, errorClass: errorClass, cacheHit: false}
}

func inferSuccess(matched bool) inferResult {
	return inferResult{matched: matched, errored: false, errorClass: "", cacheHit: false}
}

func emptyInferResult() inferResult {
	return inferResult{matched: false, errored: false, errorClass: "", cacheHit: false}
}

func (runtime *InferRuntime) contextJSON(
	ctx context.Context,
	condition *config.Condition,
	contextWorkspace string,
	contextSession string,
) (string, string) {
	if condition.ContextSource == "" {
		return "", ""
	}
	client, err := runtime.contextClient(condition.ContextEndpoint)
	if err != nil {
		return "", "context_unavailable"
	}
	turnBudget, turnBudgetValid := checkedInt32(condition.ContextTurnBudget)
	maxCharsPerTurn, maxCharsValid := checkedInt32(condition.ContextMaxCharsPerTurn)
	if !turnBudgetValid || !maxCharsValid {
		return "", "context_invalid"
	}
	reply, err := client.GetRecentTurns(ctx, &contextpb.GetRecentTurnsRequest{
		WorkspaceRef:    contextWorkspace,
		SessionRef:      contextSession,
		TurnBudget:      turnBudget,
		MaxCharsPerTurn: maxCharsPerTurn,
	})
	if err != nil || reply == nil {
		return "", "context_unavailable"
	}
	type opaqueTurn struct {
		Role string `json:"role"`
		Text string `json:"text"`
		TS   string `json:"ts"`
	}
	type opaqueContext struct {
		Turns []opaqueTurn `json:"turns"`
	}
	value := opaqueContext{Turns: make([]opaqueTurn, 0, len(reply.GetTurns()))}
	for _, turn := range reply.GetTurns() {
		if turn != nil {
			value.Turns = append(value.Turns, opaqueTurn{Role: turn.GetRole(), Text: turn.GetText(), TS: turn.GetTs()})
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", "context_invalid"
	}
	return string(encoded), ""
}

func checkedInt32(value int) (int32, bool) {
	if value < math.MinInt32 || value > math.MaxInt32 {
		return 0, false
	}
	return int32(value), true
}

func (runtime *InferRuntime) inferenceClient(endpoint string) (inferencepb.InferenceClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if client := runtime.inferenceClients[endpoint]; client != nil {
		return client, nil
	}
	connection, err := grpc.NewClient(grpcEndpoint(endpoint), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		runtime.log.Warn("create inference client failed", "endpoint", endpoint, "err", err)
		return nil, fmt.Errorf("create inference client: %w", err)
	}
	runtime.inferenceConnections[endpoint] = connection
	client := inferencepb.NewInferenceClient(connection)
	runtime.inferenceClients[endpoint] = client
	return client, nil
}

func (runtime *InferRuntime) contextClient(endpoint string) (contextpb.ConversationContextClient, error) {
	endpoint = strings.TrimSpace(endpoint)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if client := runtime.contextClients[endpoint]; client != nil {
		return client, nil
	}
	connection, err := grpc.NewClient(grpcEndpoint(endpoint), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		runtime.log.Warn("create context client failed", "endpoint", endpoint, "err", err)
		return nil, fmt.Errorf("create context client: %w", err)
	}
	runtime.contextConnections[endpoint] = connection
	client := contextpb.NewConversationContextClient(connection)
	runtime.contextClients[endpoint] = client
	return client, nil
}

func grpcEndpoint(endpoint string) string {
	if strings.HasPrefix(strings.TrimSpace(endpoint), "/") {
		return "unix://" + strings.TrimSpace(endpoint)
	}
	return strings.TrimSpace(endpoint)
}

func (runtime *InferRuntime) singleflight(ctx context.Context, key string, function func() inferResult) inferResult {
	runtime.mu.Lock()
	if flight := runtime.inflight[key]; flight != nil {
		runtime.mu.Unlock()
		select {
		case <-flight.done:
			return flight.result
		case <-ctx.Done():
			return inferError(grpcErrorClass(ctx.Err()))
		}
	}
	flight := &inferFlight{done: make(chan struct{}), result: emptyInferResult()}
	runtime.inflight[key] = flight
	runtime.mu.Unlock()
	flight.result = function()
	runtime.mu.Lock()
	delete(runtime.inflight, key)
	close(flight.done)
	runtime.mu.Unlock()
	return flight.result
}

func (runtime *InferRuntime) cacheLookup(key string) (inferResult, bool) {
	if runtime.cache == nil {
		return emptyInferResult(), false
	}
	entry, found, err := runtime.cache.Get(inferenceCacheNamespace, key)
	if err != nil || !found {
		return emptyInferResult(), false
	}
	var cached cachedInferResult
	if json.Unmarshal(entry.Value, &cached) != nil {
		_, _ = runtime.cache.Delete(inferenceCacheNamespace, key)
		return emptyInferResult(), false
	}
	return inferSuccess(cached.Matched), true
}

func (runtime *InferRuntime) cacheStore(key string, ttlMilliseconds int, result inferResult) {
	encoded, err := json.Marshal(cachedInferResult{Matched: result.matched})
	if err != nil {
		return
	}
	_, _, err = runtime.cache.Set(inferenceCacheNamespace, key, encoded, hotkv.SetOptions{
		Mode: hotkv.SetModeAny, TTL: time.Duration(ttlMilliseconds) * time.Millisecond,
	})
	if err != nil {
		runtime.log.Warn("store inference cache result failed", "status_class", "cache_error", "err", err)
	}
}

func stableInferenceKey(
	condition *config.Condition,
	input string,
	selectedKey string,
	contextWorkspace string,
	contextSession string,
) string {
	hash := sha256.New()
	parts := []string{
		condition.Endpoint, condition.LayerName, condition.Prompt, condition.OutputSchema,
		condition.Model, condition.InputField, input, condition.CacheKey, selectedKey,
		condition.ResponseJSONField, string(condition.ResponseJSONEqualsValue().Kind()),
		condition.ResponseJSONEqualsValue().CanonicalString(), condition.BlockOn, condition.OnError,
		strconv.Itoa(condition.TimeoutMs), strconv.Itoa(condition.CacheTTLMs), condition.ContextSource,
		condition.ContextEndpoint, condition.ContextWorkspaceField, condition.ContextSessionField,
		contextWorkspace, contextSession,
		strconv.Itoa(condition.ContextTurnBudget), strconv.Itoa(condition.ContextMaxCharsPerTurn), condition.ContextOnError,
	}
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func inferenceJSONMatches(condition *config.Condition, document string) (bool, error) {
	current := json.RawMessage(document)
	for part := range strings.SplitSeq(condition.ResponseJSONField, ".") {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(current, &value); err != nil {
			return false, errors.New("invalid JSON")
		}
		next, ok := value[part]
		if !ok {
			return false, errors.New("missing JSON field")
		}
		current = next
	}
	expected := condition.ResponseJSONEqualsValue()
	switch expected.Kind() {
	case config.TOMLScalarUnset:
		return false, errors.New("unsupported scalar")
	case config.TOMLScalarBool:
		var actual bool
		if err := json.Unmarshal(current, &actual); err != nil {
			return false, errors.New("decode boolean response")
		}
		return actual == expected.BoolValue(), nil
	case config.TOMLScalarString:
		var actual string
		if err := json.Unmarshal(current, &actual); err != nil {
			return false, errors.New("decode string response")
		}
		return actual == expected.StringValue(), nil
	case config.TOMLScalarInt:
		var actual json.Number
		decoder := json.NewDecoder(bytes.NewReader(current))
		decoder.UseNumber()
		if err := decoder.Decode(&actual); err != nil {
			return false, errors.New("decode integer response")
		}
		value, err := actual.Int64()
		if err == nil {
			return value == expected.IntValue(), nil
		}
		floatValue, floatErr := actual.Float64()
		if floatErr != nil {
			return false, errors.New("decode integer response number")
		}
		return floatValue == float64(expected.IntValue()), nil
	case config.TOMLScalarFloat:
		var actual json.Number
		decoder := json.NewDecoder(bytes.NewReader(current))
		decoder.UseNumber()
		if err := decoder.Decode(&actual); err != nil {
			return false, errors.New("decode float response")
		}
		value, err := actual.Float64()
		if err == nil {
			return value == expected.FloatValue(), nil
		}
		integerValue, integerErr := strconv.ParseInt(actual.String(), 10, 64)
		if integerErr != nil {
			return false, errors.New("decode float response integer")
		}
		return float64(integerValue) == expected.FloatValue(), nil
	default:
		return false, errors.New("unsupported scalar")
	}
}

func grpcErrorClass(err error) string {
	if err == nil {
		return ""
	}
	switch status.Code(err) {
	case codes.Canceled:
		return "canceled"
	case codes.DeadlineExceeded:
		return "deadline_exceeded"
	case codes.InvalidArgument:
		return "invalid_argument"
	case codes.FailedPrecondition:
		return "failed_precondition"
	case codes.Unavailable:
		return "unavailable"
	case codes.OK, codes.Unknown, codes.NotFound, codes.AlreadyExists,
		codes.PermissionDenied, codes.ResourceExhausted, codes.Aborted,
		codes.OutOfRange, codes.Unimplemented, codes.Internal, codes.DataLoss,
		codes.Unauthenticated:
		return "rpc_error"
	default:
		return "rpc_error"
	}
}

func (runtime *InferRuntime) collectTrace(ctx context.Context, condition *config.Condition, conditionIndex int, result inferResult, latency time.Duration) {
	collector, _ := ctx.Value(inferenceTraceCollectorKey{}).(InferenceTraceCollector)
	if collector == nil {
		return
	}
	outcome := "nonmatched"
	statusValue := "complete"
	if result.matched {
		outcome = "matched"
	}
	if result.errored {
		outcome = "nonmatched"
		statusValue = "error"
	}
	collector.CollectInferenceTrace(InferenceTrace{
		LayerName: condition.LayerName, ConditionIndex: conditionIndex, Model: condition.Model,
		Endpoint: condition.Endpoint, Outcome: outcome, Status: statusValue, Latency: latency,
		CacheHit: result.cacheHit, ErrorClass: result.errorClass,
	})
}
