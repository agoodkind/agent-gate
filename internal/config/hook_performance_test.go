package config_test

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

func TestHookInferencePhaseTimeoutDefaultsAndOverrides(t *testing.T) {
	setConfigHome(t, ``)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.HookInferencePhaseTimeout() != 4*time.Second {
		t.Fatalf("HookInferencePhaseTimeout = %s, want 4s", cfg.HookInferencePhaseTimeout())
	}

	setConfigHome(t, `
[performance.hook]
inference_phase_timeout_ms = 1250
`)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() override error: %v", err)
	}
	if cfg.HookInferencePhaseTimeout() != 1250*time.Millisecond {
		t.Fatalf("HookInferencePhaseTimeout override = %s, want 1.25s", cfg.HookInferencePhaseTimeout())
	}
}

func TestHookInferencePhaseTimeoutRejectsValuesAboveMaximum(t *testing.T) {
	setConfigHome(t, `
[performance.hook]
inference_phase_timeout_ms = 4001
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for inference phase timeout above 4000ms")
	}
	if !strings.Contains(err.Error(), "inference_phase_timeout_ms must not exceed 4000") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestHookInferencePhaseTimeoutCapsProgrammaticConfig(t *testing.T) {
	cfg := &config.Config{
		Performance: config.Performance{
			Hook: config.HookPerformance{InferencePhaseTimeoutMS: 5000},
		},
	}

	if cfg.HookInferencePhaseTimeout() != 4*time.Second {
		t.Fatalf("HookInferencePhaseTimeout = %s, want capped 4s", cfg.HookInferencePhaseTimeout())
	}
}
