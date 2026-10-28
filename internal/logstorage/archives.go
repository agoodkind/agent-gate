package logstorage

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const diagnosticTimestampFormat = "2006-01-02T15-04-05.000"

type diagnosticArchive struct {
	name string
	at   time.Time
}

func diagnosticArchiveDate(entry os.DirEntry) (time.Time, bool, error) {
	name := entry.Name()
	if name == DiagnosticArchiveName {
		info, err := entry.Info()
		if err != nil {
			return time.Time{}, false, logError("stat diagnostic backup", err)
		}
		return info.ModTime(), true, nil
	}
	timestamp := strings.TrimSuffix(name, ".gz")
	if !strings.HasPrefix(timestamp, "agent-gate-") || !strings.HasSuffix(timestamp, ".jsonl") {
		return time.Time{}, false, nil
	}
	timestamp = strings.TrimSuffix(strings.TrimPrefix(timestamp, "agent-gate-"), ".jsonl")
	// Timestamp archives omit a zone. UTC preserves their relative order.
	at, err := time.Parse(diagnosticTimestampFormat, timestamp)
	return at, err == nil, nil
}

func (writer *DiagnosticWriter) findArchives() ([]diagnosticArchive, error) {
	entries, err := writer.directory.ReadDir(-1)
	if err != nil {
		return nil, logError("read diagnostic archives", err)
	}
	archives := make([]diagnosticArchive, 0)
	for _, entry := range entries {
		at, owned, err := diagnosticArchiveDate(entry)
		if err != nil {
			return nil, err
		}
		if owned {
			archives = append(archives, diagnosticArchive{entry.Name(), at})
		}
	}
	slices.SortFunc(archives, func(left, right diagnosticArchive) int { return right.at.Compare(left.at) })
	return archives, nil
}

func (writer *DiagnosticWriter) validateArchive(name string) error {
	file, err := openFile(writer.directory, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	return closeFile(file)
}

func (writer *DiagnosticWriter) importCompressedArchive(file *os.File) (result error) {
	compressed, err := gzip.NewReader(io.LimitReader(file, DiagnosticMaxBytes))
	if err != nil {
		return logError("read compressed diagnostic archive", err)
	}
	defer func() {
		result = errors.Join(result, logError("close compressed diagnostic archive", compressed.Close()))
	}()
	return writer.compress(compressed)
}

func (writer *DiagnosticWriter) importArchive(name string) (result error) {
	if name == DiagnosticArchiveName {
		return nil
	}
	file, err := openFile(writer.directory, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeFile(file)) }()
	if strings.HasSuffix(name, ".gz") {
		return writer.importCompressedArchive(file)
	}
	data, err := readTail(file, DiagnosticMaxBytes)
	if err != nil {
		return err
	}
	return writer.compress(bytes.NewReader(data))
}

func (writer *DiagnosticWriter) pruneArchives() error {
	archives, err := writer.findArchives()
	if err != nil {
		return err
	}
	if len(archives) == 0 {
		return nil
	}
	for _, archive := range archives {
		if err := writer.validateArchive(archive.name); err != nil {
			return err
		}
	}
	if err := writer.importArchive(archives[0].name); err != nil {
		return err
	}
	for _, archive := range archives {
		if archive.name == DiagnosticArchiveName {
			continue
		}
		if err := unix.Unlinkat(int(writer.directory.Fd()), archive.name, 0); err != nil {
			return logError("remove diagnostic archive "+archive.name, err)
		}
	}
	return nil
}
