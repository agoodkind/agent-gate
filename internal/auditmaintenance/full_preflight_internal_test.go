package auditmaintenance

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	installer "goodkind.io/agent-gate/internal/install"
)

func TestFullCompactRequiredSpaceUsesTenPercentForLargeDatabase(t *testing.T) {
	const databaseBytes = uint64(1024 * 1024 * 1024)
	requiredBytes, err := requiredFullCompactFreeBytes(databaseBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := databaseBytes + databaseBytes/10
	if requiredBytes != want {
		t.Fatalf("required bytes = %d, want %d", requiredBytes, want)
	}
}

func TestFullCompactPreflightRejectsPathSwapDuringMeasurement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "audit.db")
	replacement := filepath.Join(directory, "replacement.db")
	createFullCompactIdentityDatabase(t, path, 1)
	createFullCompactIdentityDatabase(t, replacement, 512)
	_, err := PreviewFullCompact(t.Context(), FullCompactOptions{
		Path: path,
		Service: installer.ServiceState{
			Platform: "launchd", Managed: true, Running: true,
		},
		FreeBytes: func(string) (uint64, error) {
			if renameErr := os.Rename(path, path+".original"); renameErr != nil {
				t.Fatal(renameErr)
			}
			if renameErr := os.Rename(replacement, path); renameErr != nil {
				t.Fatal(renameErr)
			}
			return 1 << 40, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("PreviewFullCompact error = %v, want path-swap rejection", err)
	}
}

func createFullCompactIdentityDatabase(t *testing.T, path string, rows int) {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(`create table payload(value blob not null)`); err != nil {
		t.Fatal(err)
	}
	for range rows {
		if _, err := database.Exec(`insert into payload values (zeroblob(4096))`); err != nil {
			t.Fatal(err)
		}
	}
}
