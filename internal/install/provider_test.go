package installer

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseProvidersReturnsCanonicalOrder(t *testing.T) {
	providers, err := ParseProviders("copilot,claude,gemini,codex,cursor")
	if err != nil {
		t.Fatalf("ParseProviders: %v", err)
	}
	if want := AllProviders(); !reflect.DeepEqual(providers, want) {
		t.Fatalf("providers = %v, want %v", providers, want)
	}

	providers[0] = ProviderCopilot
	if got := AllProviders()[0]; got != ProviderClaude {
		t.Fatalf("AllProviders returned shared storage: first = %q", got)
	}
}

func TestParseProvidersRejectsUnknownAndDuplicateNames(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "unknown", value: "claude,other", want: "unknown provider"},
		{name: "duplicate", value: "cursor,claude,cursor", want: "duplicate provider"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseProviders(test.value)
			if err == nil {
				t.Fatal("ParseProviders returned nil error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestParseProvidersEmptyReturnsExplicitNone(t *testing.T) {
	providers, err := ParseProviders("")
	if err != nil {
		t.Fatalf("ParseProviders: %v", err)
	}
	if providers == nil || len(providers) != 0 {
		t.Fatalf("providers = %#v, want nonnil empty selection", providers)
	}
}
