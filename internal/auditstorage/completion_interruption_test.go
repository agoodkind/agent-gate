package auditstorage_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestInterruptedJournalConversionRecoversAfterRestart(t *testing.T) {
	if phase := os.Getenv("AGENT_GATE_FINALIZATION_PHASE"); phase != "" {
		runFinalizationProcess(t, phase, os.Getenv("AGENT_GATE_FINALIZATION_ROOT"))
		return
	}

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestInterruptedJournalConversionRecoversAfterRestart$",
	)
	command.Env = append(
		os.Environ(),
		"AGENT_GATE_FINALIZATION_PHASE=write",
		"AGENT_GATE_FINALIZATION_ROOT="+root,
	)
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = command.Process.Kill() })
	reader := bufio.NewReader(output)
	line, err := reader.ReadString('\n')
	if err != nil || line != "conversion unfinished\n" {
		remainder, readErr := io.ReadAll(reader)
		waitErr := command.Wait()
		t.Fatalf(
			"writer readiness = %q, %v: %s%s; read=%v wait=%v",
			line,
			err,
			remainder,
			diagnostics.String(),
			readErr,
			waitErr,
		)
	}

	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}

	if err := command.Wait(); err == nil || command.ProcessState.ExitCode() != -1 {
		t.Fatalf("writer did not terminate by signal: %v", err)
	}

	restarted := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^TestInterruptedJournalConversionRecoversAfterRestart$",
	)
	restarted.Env = append(
		os.Environ(),
		"AGENT_GATE_FINALIZATION_PHASE=recover",
		"AGENT_GATE_FINALIZATION_ROOT="+root,
	)
	if output, err := restarted.CombinedOutput(); err != nil {
		t.Fatalf("restarted finalization: %v\n%s", err, output)
	}
}

func runFinalizationProcess(t *testing.T, phase, root string) {
	t.Helper()
	if root == "" {
		t.Fatal("finalization process requires a storage directory")
	}

	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{
		BasePath:         filepath.Join(root, "audit.db"),
		StatePath:        filepath.Join(root, "state.json"),
		CoordinationPath: filepath.Join(root, "catalog.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}

	policy := auditstorage.RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	old := auditstorage.Bucket{
		ID:    "20261028T000000Z",
		Path:  filepath.Join(root, "audit-20261028T000000Z.db"),
		Start: now,
	}
	if phase == "write" {
		if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
			t.Fatal(err)
		}

		if _, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}

		database, err := auditstorage.OpenWriter(t.Context(), old.Path)
		if err != nil {
			t.Fatal(err)
		}

		if err := audit.WriteEvents(
			t.Context(),
			database,
			[]audit.Event{{EventID: "preserved", SchemaVersion: 1}},
		); err != nil {
			t.Fatal(err)
		}

		transaction, err := database.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}

		var count int
		if err := transaction.QueryRowContext(
			t.Context(),
			readFixture(t, "application_table_count.sql"),
		).Scan(
			&count,
		); err != nil {
			t.Fatal(err)
		}

		if complete, err := catalog.Finalize(t.Context(), old); complete ||
			!errors.Is(err, auditstorage.ErrFileBusy) {
			t.Fatalf("blocked conversion = %v, %v", complete, err)
		}

		if complete, err := auditstorage.Completed(old.Path); complete || err != nil {
			t.Fatalf("unfinished completion header = %v, %v", complete, err)
		}

		if info, err := os.Stat(old.Path + "-wal"); err != nil || info.Size() == 0 {
			t.Fatalf("unfinished WAL = %v, %v", info, err)
		}

		fmt.Println("conversion unfinished")
		<-t.Context().Done()

		return
	}

	if phase != "recover" {
		t.Fatalf("unknown finalization phase %q", phase)
	}

	assertPreservedAuditEvent(t, catalog, policy, now.Add(24*time.Hour))

	for range 2 {
		if complete, err := catalog.Finalize(t.Context(), old); !complete || err != nil {
			t.Fatalf("restarted conversion = %v, %v", complete, err)
		}

		assertPreservedAuditEvent(t, catalog, policy, now.Add(24*time.Hour))
		assertCompletedSidecarsAbsent(t, old.Path)
	}
}

func assertPreservedAuditEvent(
	t *testing.T,
	catalog *auditstorage.Catalog,
	policy auditstorage.RotationPolicy,
	now time.Time,
) {
	t.Helper()
	readers, err := catalog.Read(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = readers.Close() }()
	count := 0

	for _, reader := range readers.Handles {
		var found int
		if err := reader.Database.QueryRowContext(
			t.Context(),
			readFixture(t, "preserved_audit_event.sql"),
			"preserved",
		).Scan(
			&found,
		); err != nil {
			t.Fatal(err)
		}

		count += found
	}

	if count != 1 {
		t.Fatalf("preserved audit event count = %d, want 1", count)
	}
}
