package config

import (
	"fmt"
	"maps"
	"time"
)

const (
	// defaultJudgeTranscriptMaxTokens bounds the conversation tail the batch judge
	// reads when the config sets an endpoint but leaves the budget unset.
	defaultJudgeTranscriptMaxTokens = 2000
	// defaultJudgeTranscriptTimeout bounds the transcript fetch so a hung clyde
	// stream cannot stall the gated tool call.
	defaultJudgeTranscriptTimeout = 1500 * time.Millisecond
)

const (
	// defaultJudgeCloudModel names the enforcing cloud judge model priced by
	// default so the cost report estimates spend without requiring config.
	defaultJudgeCloudModel = "gpt-5.4-mini"
	// defaultJudgeCloudInputPerMillion is the gpt-5.4-mini input price in US
	// dollars per one million prompt tokens.
	defaultJudgeCloudInputPerMillion = 0.15
	// defaultJudgeCloudCachedPerMillion is the gpt-5.4-mini cached-input price in
	// US dollars per one million prompt tokens served from the provider prompt cache.
	defaultJudgeCloudCachedPerMillion = 0.015
	// defaultJudgeCloudOutputPerMillion is the gpt-5.4-mini output price in US
	// dollars per one million completion tokens.
	defaultJudgeCloudOutputPerMillion = 0.60
	// defaultJudgeLocalModel names the local record-only judge model, which runs
	// on owned hardware and therefore bills nothing.
	defaultJudgeLocalModel = "agentgate/agent-gate-judge-v4"
)

// ModelPrice is the billed price of one judge model in US dollars per one million
// tokens, split into full-rate input, provider-cached input, and output. A local
// model that runs on owned hardware sets every field to zero.
type ModelPrice struct {
	InputPerMillion       float64 `toml:"input_per_million"`
	CachedInputPerMillion float64 `toml:"cached_input_per_million"`
	OutputPerMillion      float64 `toml:"output_per_million"`
}

// Judge holds settings for the batch LLM judge decoded from the [judge] TOML
// table. The transcript tail is fetched once per command from clyde and shared
// across every rule judged in that command, so these settings are judge-level
// rather than per-inference-point. A transcript outage fails open to an empty
// tail, so on_error currently records intent without blocking the command.
type Judge struct {
	// TranscriptEndpoint is the clyde ClydeService address the judge fetches the
	// conversation tail from. Empty disables the transcript fetch, so the judge
	// reasons over the directory, command, and structural parse alone.
	TranscriptEndpoint string `toml:"transcript_endpoint"`
	// TranscriptMaxTokens is the token budget for the conversation tail.
	TranscriptMaxTokens int `toml:"transcript_max_tokens"`
	// TranscriptTokenModel names the tokenizer clyde counts the budget with. Empty
	// lets clyde derive it from the conversation's provider.
	TranscriptTokenModel string `toml:"transcript_token_model"`
	// TranscriptTimeoutMS bounds the transcript fetch in milliseconds.
	TranscriptTimeoutMS int `toml:"transcript_timeout_ms"`
	// TranscriptOnError selects the transcript-outage policy. Empty and "open"
	// proceed with an empty tail; "closed" is reserved and currently proceeds the
	// same way, because a transcript outage must not block or error the judge.
	TranscriptOnError string `toml:"transcript_on_error"`
	// Pricing overrides the built-in judge per-model price table, keyed by the
	// model name recorded on inference layers. The cost report reads it to
	// estimate billed spend; a model left out of both config and defaults costs
	// nothing in the report.
	Pricing map[string]ModelPrice `toml:"pricing,omitempty"`
}

// JudgePricing returns the judge per-model price table, built from the priced
// defaults and overlaid with any [judge.pricing] overrides, so the cost report
// can estimate spend even when config sets no prices. The returned map is a fresh
// copy the caller may retain.
func (c *Config) JudgePricing() map[string]ModelPrice {
	pricing := map[string]ModelPrice{
		defaultJudgeCloudModel: {
			InputPerMillion:       defaultJudgeCloudInputPerMillion,
			CachedInputPerMillion: defaultJudgeCloudCachedPerMillion,
			OutputPerMillion:      defaultJudgeCloudOutputPerMillion,
		},
		defaultJudgeLocalModel: {
			InputPerMillion:       0,
			CachedInputPerMillion: 0,
			OutputPerMillion:      0,
		},
	}
	if c == nil {
		return pricing
	}
	maps.Copy(pricing, c.Judge.Pricing)
	return pricing
}

// JudgeTranscriptEndpoint returns the configured clyde transcript endpoint.
func (c *Config) JudgeTranscriptEndpoint() string {
	if c == nil {
		return ""
	}
	return c.Judge.TranscriptEndpoint
}

// JudgeTranscriptMaxTokens returns the transcript token budget, defaulting when
// the config leaves it unset.
func (c *Config) JudgeTranscriptMaxTokens() int {
	if c != nil && c.Judge.TranscriptMaxTokens > 0 {
		return c.Judge.TranscriptMaxTokens
	}
	return defaultJudgeTranscriptMaxTokens
}

// JudgeTranscriptTokenModel returns the tokenizer model the budget counts with.
func (c *Config) JudgeTranscriptTokenModel() string {
	if c == nil {
		return ""
	}
	return c.Judge.TranscriptTokenModel
}

// JudgeTranscriptTimeout returns the bounded deadline for the transcript fetch,
// defaulting when the config leaves it unset.
func (c *Config) JudgeTranscriptTimeout() time.Duration {
	if c != nil && c.Judge.TranscriptTimeoutMS > 0 {
		return time.Duration(c.Judge.TranscriptTimeoutMS) * time.Millisecond
	}
	return defaultJudgeTranscriptTimeout
}

// JudgeTranscriptOnError returns the configured transcript-outage policy.
func (c *Config) JudgeTranscriptOnError() string {
	if c == nil {
		return ""
	}
	return c.Judge.TranscriptOnError
}

// validateJudge rejects a negative transcript budget or timeout and an unknown
// on_error value, so a malformed [judge] table fails the config load.
func validateJudge(judge Judge) error {
	if judge.TranscriptMaxTokens < 0 {
		return fmt.Errorf("judge.transcript_max_tokens must be non-negative")
	}
	if judge.TranscriptTimeoutMS < 0 {
		return fmt.Errorf("judge.transcript_timeout_ms must be non-negative")
	}
	if !validContextOnError(judge.TranscriptOnError) {
		return fmt.Errorf(
			"judge.transcript_on_error %q must be %q, %q, or empty",
			judge.TranscriptOnError, OnErrorOpen, OnErrorClosed,
		)
	}
	for model, price := range judge.Pricing {
		if price.InputPerMillion < 0 ||
			price.CachedInputPerMillion < 0 ||
			price.OutputPerMillion < 0 {
			return fmt.Errorf("judge.pricing[%q] prices must be non-negative", model)
		}
	}
	return nil
}
