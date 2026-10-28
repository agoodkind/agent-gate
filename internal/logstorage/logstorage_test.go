package logstorage_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/logstorage"
)

func TestDiagnosticRotatesWithOneCompressedBackup(t *testing.T) {
	directory := t.TempDir()
	writer, err := logstorage.OpenDiagnostic(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for _, character := range []string{"a", "b", "c", "d"} {
		record := strings.Repeat(character, 600000) + "\n"
		if _, err := writer.Write([]byte(record)); err != nil {
			t.Fatal(err)
		}
	}
	current := readFile(t, filepath.Join(directory, logstorage.DiagnosticName))
	if !bytes.Equal(current, []byte(strings.Repeat("d", 600000)+"\n")) {
		t.Fatal("the active diagnostic log does not contain the latest record")
	}
	backup := readGzip(t, filepath.Join(directory, logstorage.DiagnosticArchiveName))
	if !bytes.Equal(backup, []byte(strings.Repeat("c", 600000)+"\n")) {
		t.Fatal("the diagnostic backup does not contain the previous record")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("diagnostic files = %d, want active log, backup, and lock", len(entries))
	}
}

func TestDiagnosticStartupPrunesExistingArchives(t *testing.T) {
	directory := t.TempDir()
	legacy := []string{
		"agent-gate-2025-01-01T01-00-00.000.jsonl",
		"agent-gate-2025-01-02T01-00-00.000.jsonl",
		"agent-gate-2025-01-03T01-00-00.000.jsonl.gz",
	}
	writeFile(t, filepath.Join(directory, legacy[0]), []byte("first\n"))
	writeFile(t, filepath.Join(directory, legacy[1]), []byte("second\n"))
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	if _, err := gzipWriter.Write([]byte("latest\n")); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(directory, legacy[2]), compressed.Bytes())
	other := filepath.Join(directory, "agent-gate-not-an-archive.jsonl")
	writeFile(t, other, []byte("unrelated\n"))
	writer, err := logstorage.OpenDiagnostic(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range legacy {
		if _, err := os.Stat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			t.Fatalf("startup did not remove %s: %v", name, err)
		}
	}
	if got := string(readGzip(t, filepath.Join(directory, logstorage.DiagnosticArchiveName))); got != "latest\n" {
		t.Fatalf("backup = %q, want latest archive", got)
	}
	if got := string(readFile(t, other)); got != "unrelated\n" {
		t.Fatalf("unrelated content = %q", got)
	}
}

func TestDiagnosticStartupBoundsExistingActiveLog(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, logstorage.DiagnosticName)
	writeFile(t, path, []byte(strings.Repeat("old\n", logstorage.DiagnosticMaxBytes)+"newest\n"))
	writer, err := logstorage.OpenDiagnostic(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if info, err := os.Stat(path); err != nil || info.Size() > logstorage.DiagnosticMaxBytes {
		t.Fatalf("active log exceeds its limit: %v, %v", info, err)
	}
	backup := readGzip(t, filepath.Join(directory, logstorage.DiagnosticArchiveName))
	if len(backup) > logstorage.DiagnosticMaxBytes || !bytes.HasSuffix(backup, []byte("newest\n")) {
		t.Fatal("startup backup does not retain a bounded copy of the latest bytes")
	}
}

func TestMaintainPreservesSupervisorDescriptor(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, logstorage.SupervisorName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := file.WriteString(strings.Repeat("old line\n", logstorage.SupervisorMaxBytes/4) + "newest line\n"); err != nil {
			t.Fatal(err)
		}
		if err := logstorage.Maintain(directory); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(before, after) || after.Size() > logstorage.SupervisorMaxBytes {
			t.Fatalf("maintenance changed the inode or exceeded the size limit: %v", after)
		}
		data := readFile(t, path)
		if !bytes.HasPrefix(data, []byte("old line\n")) || !bytes.HasSuffix(data, []byte("newest line\n")) {
			t.Fatal("maintenance did not retain complete recent lines")
		}
	}
	if _, err := file.WriteString("after maintenance\n"); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(readFile(t, path), []byte("after maintenance\n")) {
		t.Fatal("the open supervisor descriptor cannot append after maintenance")
	}
}

func TestLogStorageRejectsSymlinks(t *testing.T) {
	for _, name := range []string{logstorage.SupervisorName, logstorage.FailOpenName, logstorage.DiagnosticName, logstorage.DiagnosticArchiveName} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			target := filepath.Join(t.TempDir(), "unrelated")
			original := []byte(strings.Repeat("keep\n", logstorage.FailOpenMaxBytes))
			writeFile(t, target, original)
			if err := os.Symlink(target, filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case logstorage.DiagnosticName, logstorage.DiagnosticArchiveName:
				_, err = logstorage.OpenDiagnostic(directory)
			default:
				err = logstorage.Maintain(directory)
			}
			if err == nil {
				t.Fatal("maintenance accepted a symbolic link")
			}
			if !bytes.Equal(readFile(t, target), original) {
				t.Fatal("maintenance changed the symbolic link target")
			}
		})
	}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readGzip(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
