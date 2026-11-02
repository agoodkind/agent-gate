package daemon_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestRecoveryLateLowerReceiptOverflowPublishesAnotherPass(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	harness.queueLimit = 1
	harness.writeConfig(t, 1, "late-lower")
	body, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	hotRule := strings.ReplaceAll(ownershipFixture(t, "recovery_hot.toml"), "{{ENDPOINT}}", strconv.Quote(harness.endpoint))
	body = append(body, []byte("\n"+hotRule)...)
	if err := os.WriteFile(config.Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	server, err := daemon.New(t.Context(), ownershipLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.StartAuditScheduler(t.Context())
	start := func(identifier string) <-chan error {
		done := make(chan error, 1)
		go func() {
			_, err := server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: ownershipPayload(identifier)})
			done <- err
		}()
		return done
	}
	acceptHot := func(identifier string) {
		done := start(identifier)
		call := harness.nextCall(t)
		if !strings.Contains(call.input, identifier) {
			t.Fatalf("hot request = %s, expected %s", call.input, identifier)
		}
		close(call.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	lowerDone := start("late-lower")
	lowerHot := harness.nextCall(t)
	if !strings.Contains(lowerHot.input, "late-lower") {
		t.Fatalf("lower hot request = %s", lowerHot.input)
	}
	acceptHot("higher-1")
	firstDeferred := harness.nextCall(t)
	acceptHot("higher-2")
	acceptHot("higher-3")
	accepted := 4
	completedCalls := 1
	close(firstDeferred.release)
	var recovered ownershipCall
	for attempts := range 12 {
		call := harness.nextCall(t)
		if strings.Contains(call.input, "higher-3") {
			recovered = call
			break
		}
		acceptHot(fmt.Sprintf("higher-extra-%d", attempts))
		accepted++
		completedCalls++
		close(call.release)
	}
	if recovered.release == nil {
		t.Fatal("overflow receipt did not establish recovery progress")
	}
	close(lowerHot.release)
	if err := <-lowerDone; err != nil {
		t.Fatal(err)
	}
	close(recovered.release)
	completedCalls++
	sawLower := false
	for range accepted - completedCalls {
		call := harness.nextCall(t)
		if strings.Contains(call.input, "late-lower") {
			sawLower = true
		}
		close(call.release)
	}
	if !sawLower {
		t.Fatal("late lower receipt did not complete")
	}
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{accepted, accepted, accepted, accepted} })
}
