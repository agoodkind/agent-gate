package hook_test

import (
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestTextOrObjectSearchableText_String(t *testing.T) {
	var value hook.TextOrObject
	if err := json.Unmarshal([]byte(`"plain output"`), &value); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := value.SearchableText(); got != "plain output" {
		t.Fatalf("SearchableText() = %q, want plain output", got)
	}
}

func TestTextOrObjectSearchableText_SkipsImageData(t *testing.T) {
	imageData := "eyJ" + strings.Repeat("A", 100)
	rawJSON := `{
		"content": [
			{"type": "text", "text": "visible result"},
			{"type": "image", "mimeType": "image/png", "data": "` + imageData + `"}
		],
		"duration": 1
	}`

	var value hook.TextOrObject
	if err := json.Unmarshal([]byte(rawJSON), &value); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got := value.SearchableText()
	if !strings.Contains(got, "visible result") {
		t.Fatalf("SearchableText() missing text content: %q", got)
	}
	if !strings.Contains(got, "image/png") {
		t.Fatalf("SearchableText() missing image metadata: %q", got)
	}
	if strings.Contains(got, imageData) {
		t.Fatalf("SearchableText() included image data")
	}
}

func TestTextOrObjectSearchableText_PreservesNonImageData(t *testing.T) {
	rawJSON := `{"type":"text","data":"visible data"}`

	var value hook.TextOrObject
	if err := json.Unmarshal([]byte(rawJSON), &value); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := value.SearchableText(); !strings.Contains(got, "visible data") {
		t.Fatalf("SearchableText() missing non-image data: %q", got)
	}
}
