package daemon_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestRecoveryReservesCreditDuringContinuousByteSaturation(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) { runRecoveryByteSaturation(t, workers) })
	}
}

func runRecoveryByteSaturation(t *testing.T, workers int) {
	t.Helper()
	const identifierBudget = 1024 * 1024
	const identifierOverhead = 128
	harness := newOwnershipHarness(t, workers, false, false)
	original, err := ownershipCatalog(t, harness.cfg).Status(
		harness.cfg.AuditStoragePolicy().Rotation(),
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}

	root := filepath.Dir(original.CurrentBucketPath)
	selected := false

	for length := 16; length <= 200; length++ {
		base := filepath.Join(root, strings.Repeat("d", length), "audit.db")
		currentPath := filepath.Join(filepath.Dir(base), "audit-"+original.CurrentBucketID+".db")
		charge := identifierOverhead + len(original.CurrentBucketID) + len(currentPath)
		if identifierBudget%charge < 21 {
			harness.base = base
			selected = true
			break
		}
	}

	if !selected {
		t.Fatal("cannot construct the required identifier charge")
	}

	harness.maxSize = "0B"
	harness.profile = "minimal"
	harness.queueLimit = 8192
	harness.reload(t, workers, "byte-saturation")
	harness.server.Close()
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	harness.cfg = cfg
	catalog := ownershipCatalog(t, cfg)
	current, err := catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	historical := seedRecoveryBacklog(t, harness, 1)
	statePath := cfg.AuditCatalogOptions().StatePath
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	var state auditstorage.CatalogState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}

	// Legacy rotation selected an unsuffixed current file after a segmented file.
	state.CurrentBucketID = current.CurrentBucketID
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := daemon.New(t.Context(), ownershipLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	harness.server = server
	status, err := catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil || status.CurrentBucketID != current.CurrentBucketID {
		t.Fatalf("legacy current assignment changed: %+v: %v", status, err)
	}

	charge := identifierOverhead + len(status.CurrentBucketID) + len(status.CurrentBucketPath)
	historicalCharge := identifierOverhead + len(historical.ID) + len(historical.Path)
	remainder := identifierBudget % charge
	if historicalCharge <= charge || remainder >= historicalCharge-charge {
		t.Fatalf(
			"invalid credit boundary: current=%d historical=%d free=%d",
			charge,
			historicalCharge,
			remainder,
		)
	}

	initial := identifierBudget/charge + 20
	for index := range initial {
		harness.accept(t, fmt.Sprintf("saturated-%d", index))
	}

	readers, err := catalog.Read(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = readers.Close() }()
	var currentReader *auditstorage.BucketHandle

	for _, reader := range readers.Handles {
		if reader.Bucket.ID == current.CurrentBucketID {
			currentReader = reader
		} else if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if currentReader == nil {
		t.Fatal("current reader is unavailable")
	}

	completed := func() int {
		var counts [4]int
		if err := currentReader.Database.QueryRowContext(
			t.Context(),
			ownershipFixture(t, "ownership_outcome.sql"),
		).Scan(
			&counts[0],
			&counts[1],
			&counts[2],
			&counts[3],
		); err != nil {
			t.Fatal(err)
		}

		return counts[1]
	}
	accepted := initial
	completedCurrent := 0
	found := false
	server.StartAuditScheduler(t.Context())

	for range 200 {
		call := harness.nextCall(t)
		if strings.Contains(call.input, "backlog-0") {
			found = true
			close(call.release)
			break
		}

		harness.accept(t, fmt.Sprintf("saturated-%d", accepted))
		accepted++
		close(call.release)
		completedCurrent++
		ownershipWait(t, func() bool { return completed() >= completedCurrent })
	}

	if !found {
		t.Fatal("historical receipt starved under sustained byte saturation")
	}

	for range accepted - completedCurrent {
		call := harness.nextCall(t)
		close(call.release)
	}

	total := accepted + 1
	ownershipWait(
		t,
		func() bool { return ownershipOutcomes(t, cfg) == [4]int{total, total, total, total} },
	)
	t.Logf(
		"current_charge=%d historical_charge=%d free_bytes=%d initial_receipts=%d accepted_current=%d current_calls_before_history=%d",
		charge,
		historicalCharge,
		remainder,
		initial,
		accepted,
		completedCurrent,
	)
}
