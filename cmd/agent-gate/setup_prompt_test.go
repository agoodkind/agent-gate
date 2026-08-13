package main

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/setup"
)

func TestSetupPrompterUsesDetectedProvidersAndExistingProfileAsDefaults(t *testing.T) {
	var output bytes.Buffer
	prompter := NewSetupPrompter(strings.NewReader("\n\n"), &output)
	providers, err := prompter.SelectProviders([]setup.ProviderState{
		{Provider: installer.ProviderClaude, ClientPath: "/usr/local/bin/claude"},
		{Provider: installer.ProviderCodex},
		{Provider: installer.ProviderCursor, ManagedRegistration: true},
	})
	if err != nil {
		t.Fatalf("SelectProviders: %v", err)
	}
	wantProviders := []installer.Provider{installer.ProviderClaude, installer.ProviderCursor}
	if !reflect.DeepEqual(providers, wantProviders) {
		t.Fatalf("providers = %#v, want %#v", providers, wantProviders)
	}
	profile, err := prompter.SelectAuditProfile(config.AuditStoragePolicy{
		Profile: config.AuditStorageProfileMinimal,
	})
	if err != nil {
		t.Fatalf("SelectAuditProfile: %v", err)
	}
	if profile != config.AuditStorageProfileMinimal {
		t.Fatalf("profile = %q, want minimal", profile)
	}
}

func TestSetupPrompterRetriesInvalidInput(t *testing.T) {
	var output bytes.Buffer
	prompter := NewSetupPrompter(
		strings.NewReader("unknown\ncodex,gemini\nbad\nfull\nmaybe\ny\n"),
		&output,
	)
	providers, err := prompter.SelectProviders(nil)
	if err != nil {
		t.Fatalf("SelectProviders: %v", err)
	}
	wantProviders := []installer.Provider{installer.ProviderCodex, installer.ProviderGemini}
	if !reflect.DeepEqual(providers, wantProviders) {
		t.Fatalf("providers = %#v, want %#v", providers, wantProviders)
	}
	profile, err := prompter.SelectAuditProfile(config.AuditStoragePolicy{})
	if err != nil {
		t.Fatalf("SelectAuditProfile: %v", err)
	}
	if profile != config.AuditStorageProfileFull {
		t.Fatalf("profile = %q, want full", profile)
	}
	confirmed, err := prompter.Confirm(PlanSummary{Providers: providers})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !confirmed {
		t.Fatal("confirmation = false, want true")
	}
	for _, want := range []string{"unknown provider", "select balanced, full, or minimal", "enter yes or no"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output = %q, want %q", output.String(), want)
		}
	}
}

func TestSetupPrompterReportsClosedInput(t *testing.T) {
	prompter := NewSetupPrompter(strings.NewReader(""), io.Discard)
	if _, err := prompter.SelectProviders(nil); err == nil {
		t.Fatal("SelectProviders succeeded with closed input")
	}
}
