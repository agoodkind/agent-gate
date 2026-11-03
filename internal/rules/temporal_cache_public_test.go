package rules_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func TestTemporalRemovalPreservesOtherConversationThroughValidator(t *testing.T) {
	tee, err := exec.LookPath("tee")
	if err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(t.TempDir(), "input.json")
	rule := loadExecRule(t, fmt.Sprintf(`
[[rules]]
name = "temporal-context"
events = ["stop"]
action = "inject"
output = "fallback"
[[rules.conditions]]
kind = "exec"
command = [%q, %q]
block_on = "zero"
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`, tee, capture))
	store := hotkv.New(hotkv.Options{MaxEntries: 4})
	defer store.Close()
	runtime := rules.NewExecRuntimeWithCache(nil, nil, store)
	alpha := rules.FieldSet{ConversationID: "alpha"}
	beta := rules.FieldSet{ConversationID: "beta"}
	order := func(receipt int64) hotkv.ObservationOrder {
		return hotkv.ObservationOrder{Generation: 1, ReceiptID: receipt}
	}
	if !runtime.ObserveUserPromptOrdered("cursor", alpha, order(1), "alpha prompt") ||
		!runtime.ObserveUserPromptOrdered("cursor", beta, order(2), "beta prompt") ||
		!runtime.ObserveResponseOutputOrdered("cursor", beta, "stop", "context", order(2), "beta response") {
		t.Fatal("initial temporal publication failed")
	}
	store.RegisterObservation(order(10))
	entries, err := store.List(hotkv.InternalNamespacePrefix+"exec-temporal", "", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	for _, entry := range entries {
		if string(entry.Value) == "alpha prompt" {
			removed, err = store.Delete(entry.Namespace, entry.Key)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !removed {
		t.Fatal("alpha prompt did not exist")
	}
	if runtime.ObserveUserPromptOrdered("cursor", alpha, order(4), "delayed alpha") {
		t.Fatal("removed conversation accepted delayed context")
	}
	if !runtime.ObserveUserPromptOrdered("cursor", beta, order(3), "new beta prompt") ||
		!runtime.ObserveResponseOutputOrdered("cursor", beta, "stop", "context", order(4), "new beta response") {
		t.Fatal("unrelated retained conversation lost its ordering")
	}
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	ctx = rules.WithExecResponseTargetResolver(ctx, func(string) string { return "context" })
	for _, example := range []struct {
		fields    rules.FieldSet
		prompt    string
		response  string
		available bool
	}{
		{fields: alpha, available: false},
		{fields: beta, prompt: "new beta prompt", response: "new beta response", available: true},
	} {
		rules.EvaluateAll(ctx, "cursor", "stop", example.fields, []config.Rule{rule}, nil)
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatal(err)
		}
		var input execconcern.Input
		if err := json.Unmarshal(data, &input); err != nil {
			t.Fatal(err)
		}
		if len(input.Matched) != 2 {
			t.Fatalf("matched fields: %+v", input.Matched)
		}
		assertTemporalField(t, input.Matched[0], "last_user_message", example.prompt, example.available)
		assertTemporalField(t, input.Matched[1], "last_response_output", example.response, example.available)
	}
}
