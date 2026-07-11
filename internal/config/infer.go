package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	// DefaultInferTimeoutMs is the default per-condition inference deadline.
	DefaultInferTimeoutMs = 1500
	// MaxInferTimeoutMs is the maximum per-condition inference deadline.
	MaxInferTimeoutMs = 4000
	// DefaultContextTurnBudget is the default Clyde context turn count.
	DefaultContextTurnBudget = 4
	// MaxContextTurnBudget is the maximum Clyde context turn count.
	MaxContextTurnBudget = 32
	// DefaultContextMaxCharsPerTurn is the default character limit per context turn.
	DefaultContextMaxCharsPerTurn = 280
	// MaxContextMaxCharsPerTurn is the maximum character limit per context turn.
	MaxContextMaxCharsPerTurn = 8000
)

// InputFieldSelector returns the compiled infer input selector.
func (c *Condition) InputFieldSelector() FieldSelectorSpec { return c.inputSelector }

// ContextWorkspaceSelector returns the compiled clyde workspace selector.
func (c *Condition) ContextWorkspaceSelector() FieldSelectorSpec { return c.contextWorkspaceSelector }

// ContextSessionSelector returns the compiled clyde session selector.
func (c *Condition) ContextSessionSelector() FieldSelectorSpec { return c.contextSessionSelector }

// ResponseJSONEqualsValue returns the decoded infer response predicate scalar.
func (c *Condition) ResponseJSONEqualsValue() TOMLScalarValue { return c.responseJSONValue }

func compileInferConfig(log *slog.Logger, ruleName string, index int, condition *Condition, meta toml.MetaData, configDirectory string) error {
	if ConditionKind(condition.Kind) != ConditionKindInfer {
		return nil
	}
	contextLabel := fmt.Sprintf("rule %q condition %d", ruleName, index)
	condition.Endpoint = strings.TrimSpace(condition.Endpoint)
	condition.LayerName = strings.TrimSpace(condition.LayerName)
	if condition.Endpoint == "" {
		return fmt.Errorf("%s: infer requires endpoint", contextLabel)
	}
	if condition.LayerName == "" {
		return fmt.Errorf("%s: infer requires layer_name", contextLabel)
	}
	prompt, err := compileDeclaration(configDirectory, condition.Prompt, condition.PromptFile, "prompt", "prompt_file")
	if err != nil {
		log.Warn("compile inference prompt declaration failed", "rule", ruleName, "condition_index", index, "err", err)
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	condition.Prompt = prompt
	schema, err := compileDeclaration(configDirectory, condition.OutputSchema, condition.OutputSchemaFile, "output_schema", "output_schema_file")
	if err != nil {
		log.Warn("compile inference schema declaration failed", "rule", ruleName, "condition_index", index, "err", err)
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	if !json.Valid([]byte(schema)) {
		return fmt.Errorf("%s: output_schema must be valid JSON", contextLabel)
	}
	condition.OutputSchema = schema

	condition.InputField = strings.TrimSpace(condition.InputField)
	condition.inputSelector, err = compileRequiredSelector(condition.InputField, "input_field")
	if err != nil {
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	if condition.CacheKey == "" {
		condition.CacheKey = condition.InputField
	}
	condition.cacheKeySelector, err = compileRequiredSelector(strings.TrimSpace(condition.CacheKey), "cache_key")
	if err != nil {
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	if condition.TimeoutMs == 0 {
		condition.TimeoutMs = DefaultInferTimeoutMs
	}
	if condition.TimeoutMs < 0 || condition.TimeoutMs > MaxInferTimeoutMs {
		return fmt.Errorf("%s: timeout_ms %d exceeds max %d", contextLabel, condition.TimeoutMs, MaxInferTimeoutMs)
	}
	if condition.CacheTTLMs < 0 {
		return fmt.Errorf("%s: cache_ttl_ms must be non-negative", contextLabel)
	}
	if condition.BlockOn == "" {
		condition.BlockOn = BlockOnMatch
	}
	if condition.BlockOn != BlockOnMatch && condition.BlockOn != "nonmatch" {
		return fmt.Errorf("%s: block_on %q must be %q or %q", contextLabel, condition.BlockOn, BlockOnMatch, "nonmatch")
	}
	if condition.OnError == "" {
		condition.OnError = OnErrorOpen
	}
	if condition.OnError != OnErrorOpen && condition.OnError != OnErrorClosed {
		return fmt.Errorf("%s: on_error %q must be %q or %q", contextLabel, condition.OnError, OnErrorOpen, OnErrorClosed)
	}
	condition.ResponseJSONField = strings.TrimSpace(condition.ResponseJSONField)
	if err := validateStdoutJSONFieldPath(condition.ResponseJSONField); err != nil {
		return fmt.Errorf("%s: response_json_field: %w", contextLabel, err)
	}
	if condition.ResponseJSONEquals == nil {
		return fmt.Errorf("%s: response_json_equals is required", contextLabel)
	}
	condition.responseJSONValue, err = decodeTOMLScalar(meta, *condition.ResponseJSONEquals)
	if err != nil {
		return fmt.Errorf("%s: response_json_equals: %w", contextLabel, err)
	}
	return compileInferContextConfig(log, contextLabel, condition)
}

func compileDeclaration(configDirectory string, inline string, file string, inlineName string, fileName string) (string, error) {
	hasInline := inline != ""
	hasFile := strings.TrimSpace(file) != ""
	if hasInline == hasFile {
		return "", fmt.Errorf("exactly one of %s or %s must be set", inlineName, fileName)
	}
	if hasInline {
		return inline, nil
	}
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(configDirectory, path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("read inference declaration file failed", "field", fileName, "path", path, "err", err)
		return "", fmt.Errorf("read %s: %w", fileName, err)
	}
	return string(content), nil
}

func compileRequiredSelector(path string, fieldName string) (FieldSelectorSpec, error) {
	spec := FieldSelectorSpec{Path: path, Selector: CompileFieldSelector(path)}
	if path == "" || spec.Selector == FieldSelectorInvalid {
		return spec, fmt.Errorf("%s %q is not a valid field selector", fieldName, path)
	}
	return spec, nil
}

func compileInferContextConfig(log *slog.Logger, contextLabel string, condition *Condition) error {
	condition.ContextSource = strings.TrimSpace(condition.ContextSource)
	if condition.ContextSource == "" {
		return nil
	}
	if condition.ContextSource != "clyde_recent_turns" {
		return fmt.Errorf("%s: unknown context_source %q", contextLabel, condition.ContextSource)
	}
	condition.ContextEndpoint = strings.TrimSpace(condition.ContextEndpoint)
	if condition.ContextEndpoint == "" {
		return fmt.Errorf("%s: context_endpoint is required", contextLabel)
	}
	var err error
	condition.contextWorkspaceSelector, err = compileRequiredSelector(strings.TrimSpace(condition.ContextWorkspaceField), "context_workspace_field")
	if err != nil {
		log.Warn("compile inference context workspace selector failed", "context", contextLabel, "err", err)
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	condition.contextSessionSelector, err = compileRequiredSelector(strings.TrimSpace(condition.ContextSessionField), "context_session_field")
	if err != nil {
		log.Warn("compile inference context session selector failed", "context", contextLabel, "err", err)
		return fmt.Errorf("%s: %w", contextLabel, err)
	}
	if condition.ContextTurnBudget == 0 {
		condition.ContextTurnBudget = DefaultContextTurnBudget
	}
	if condition.ContextTurnBudget < 1 || condition.ContextTurnBudget > MaxContextTurnBudget {
		return fmt.Errorf("%s: context_turn_budget must be between 1 and %d", contextLabel, MaxContextTurnBudget)
	}
	if condition.ContextMaxCharsPerTurn == 0 {
		condition.ContextMaxCharsPerTurn = DefaultContextMaxCharsPerTurn
	}
	if condition.ContextMaxCharsPerTurn < 1 || condition.ContextMaxCharsPerTurn > MaxContextMaxCharsPerTurn {
		return fmt.Errorf("%s: context_max_chars_per_turn must be between 1 and %d", contextLabel, MaxContextMaxCharsPerTurn)
	}
	if condition.ContextOnError != "empty" && condition.ContextOnError != "error" {
		return fmt.Errorf("%s: context_on_error %q must be %q or %q", contextLabel, condition.ContextOnError, "empty", "error")
	}
	return nil
}
