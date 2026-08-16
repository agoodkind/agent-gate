package hook

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestApplicableEventPairs_AllEventsExpandsRemainingProviders(t *testing.T) {
	rule := &config.Rule{AllEvents: true, DisableProviders: []string{"cursor"}}
	pairs := applicableEventPairs(rule)

	if len(pairs) == 0 {
		t.Fatal("expected non-empty applicable event pairs for all_events rule")
	}
	for _, pair := range pairs {
		if pair.system == "cursor" {
			t.Fatalf("cursor should be filtered out, got pair %#v", pair)
		}
	}
	if !slices.ContainsFunc(pairs, func(p schemaEventPair) bool {
		return p.system == "claude" && p.event == "PreToolUse"
	}) {
		t.Fatalf("expected claude PreToolUse in pairs, got %#v", pairs)
	}
}

func TestApplicableEventPairs_EmptyListsWithoutAllEventsMatchNothing(t *testing.T) {
	rule := &config.Rule{}
	pairs := applicableEventPairs(rule)
	if len(pairs) != 0 {
		t.Fatalf("expected no applicable pairs, got %#v", pairs)
	}
}

func TestApplicableEventPairs_DisableProvidersFiltersListedEvents(t *testing.T) {
	rule := &config.Rule{
		Events:           []string{"PreToolUse"},
		DisableProviders: []string{"cursor"},
	}
	pairs := applicableEventPairs(rule)

	for _, pair := range pairs {
		if pair.system == "cursor" {
			t.Fatalf("cursor should be filtered out, got pair %#v", pair)
		}
	}
	if !slices.ContainsFunc(pairs, func(p schemaEventPair) bool {
		return p.system == "claude" && p.event == "PreToolUse"
	}) {
		t.Fatalf("expected claude PreToolUse in pairs, got %#v", pairs)
	}
}

func TestRuleSubscriptions_AllEventsExpandsRemainingProviders(t *testing.T) {
	rule := &config.Rule{AllEvents: true, DisableProviders: []string{"cursor"}}
	subs := ruleSubscriptions(rule)

	if len(subs) == 0 {
		t.Fatal("expected non-empty subscriptions for all_events rule")
	}
	for _, sub := range subs {
		if sub.system == SystemCursor {
			t.Fatalf("cursor should be filtered out, got sub %#v", sub)
		}
	}
	if !slices.ContainsFunc(subs, func(s ruleSubscription) bool {
		return s.system == SystemClaude && s.event == "PreToolUse"
	}) {
		t.Fatalf("expected claude PreToolUse subscription, got %#v", subs)
	}
}

func TestRuleSubscriptions_DisableProvidersFiltersListedEvents(t *testing.T) {
	rule := &config.Rule{
		CursorEvents:     []string{"preToolUse"},
		ClaudeEvents:     []string{"PreToolUse"},
		DisableProviders: []string{"cursor"},
	}
	subs := ruleSubscriptions(rule)

	for _, sub := range subs {
		if sub.system == SystemCursor {
			t.Fatalf("cursor should be filtered out, got sub %#v", sub)
		}
	}
	if !slices.ContainsFunc(subs, func(s ruleSubscription) bool {
		return s.system == SystemClaude && s.event == "PreToolUse"
	}) {
		t.Fatalf("expected claude PreToolUse subscription, got %#v", subs)
	}
}

func TestWarnCapabilityDowngrades_AllEventsRespectsDisableProviders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Rules: []config.Rule{{
		Name:             "block-all",
		AllEvents:        true,
		DisableProviders: []string{"cursor"},
		Action:           config.ActionBlock,
	}}}
	downgrades := WarnCapabilityDowngrades(context.Background(), logger, cfg)

	for _, d := range downgrades {
		if d.System == SystemCursor {
			t.Fatalf("cursor should be filtered out, got downgrade %#v", d)
		}
	}
	if len(downgrades) == 0 {
		t.Fatal("expected downgrades for observe-only events on non-disabled providers")
	}
}
