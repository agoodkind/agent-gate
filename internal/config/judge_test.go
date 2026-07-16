package config

import "testing"

func TestJudgePricingReturnsPricedDefaults(t *testing.T) {
	var cfg Config
	pricing := cfg.JudgePricing()

	cloud, ok := pricing[defaultJudgeCloudModel]
	if !ok {
		t.Fatalf("default pricing missing cloud model %q", defaultJudgeCloudModel)
	}
	if cloud.InputPerMillion != defaultJudgeCloudInputPerMillion ||
		cloud.CachedInputPerMillion != defaultJudgeCloudCachedPerMillion ||
		cloud.OutputPerMillion != defaultJudgeCloudOutputPerMillion {
		t.Fatalf("cloud default price = %+v", cloud)
	}

	local, ok := pricing[defaultJudgeLocalModel]
	if !ok {
		t.Fatalf("default pricing missing local model %q", defaultJudgeLocalModel)
	}
	if local.InputPerMillion != 0 || local.CachedInputPerMillion != 0 || local.OutputPerMillion != 0 {
		t.Fatalf("local default price = %+v, want all zero", local)
	}
}

func TestJudgePricingOverlaysConfiguredOverrides(t *testing.T) {
	cfg := Config{Judge: Judge{Pricing: map[string]ModelPrice{
		defaultJudgeCloudModel: {InputPerMillion: 1, CachedInputPerMillion: 0.1, OutputPerMillion: 2},
		"custom/model":         {InputPerMillion: 5, CachedInputPerMillion: 0.5, OutputPerMillion: 9},
	}}}
	pricing := cfg.JudgePricing()

	cloud := pricing[defaultJudgeCloudModel]
	if cloud.InputPerMillion != 1 || cloud.OutputPerMillion != 2 {
		t.Fatalf("override not applied to cloud model: %+v", cloud)
	}
	custom, ok := pricing["custom/model"]
	if !ok || custom.InputPerMillion != 5 || custom.OutputPerMillion != 9 {
		t.Fatalf("configured custom model missing or wrong: %+v ok=%v", custom, ok)
	}
	if _, ok := pricing[defaultJudgeLocalModel]; !ok {
		t.Fatalf("overlay dropped the default local model")
	}
}

func TestValidateJudgeRejectsNegativePrice(t *testing.T) {
	err := validateJudge(Judge{Pricing: map[string]ModelPrice{
		"gpt-5.4-mini": {InputPerMillion: -0.1},
	}})
	if err == nil {
		t.Fatal("validateJudge accepted a negative price")
	}
}

func TestValidateJudgeAcceptsNonNegativePrice(t *testing.T) {
	err := validateJudge(Judge{Pricing: map[string]ModelPrice{
		"gpt-5.4-mini": {InputPerMillion: 0.15, CachedInputPerMillion: 0.015, OutputPerMillion: 0.6},
	}})
	if err != nil {
		t.Fatalf("validateJudge rejected a valid price table: %v", err)
	}
}
