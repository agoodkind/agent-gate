package logstorage

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// DiagnosticArchiveName identifies the single compressed diagnostic backup.
const DiagnosticArchiveName = "agent-gate.previous.jsonl.gz"

// DiagnosticWriter rotates diagnostic records and keeps one compressed backup.
type DiagnosticWriter struct {
	mu        sync.Mutex
	directory *os.File
}

// OpenDiagnostic prunes existing diagnostic archives before accepting records.
func OpenDiagnostic(stateDir string) (*DiagnosticWriter, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, wrapError("create diagnostic log directory", err)
	}
	directory, err := openDirectory(stateDir)
	if err != nil {
		return nil, err
	}
	writer := &DiagnosticWriter{mu: sync.Mutex{}, directory: directory}
	err = writer.locked(func() error {
		if err := writer.pruneArchives(); err != nil {
			return err
		}
		return withLockedFile(directory, DiagnosticName, unix.O_RDWR|unix.O_CREAT, func(file *os.File) error {
			info, err := file.Stat()
			if err != nil {
				return wrapError("stat existing diagnostic log", err)
			}
			if info.Size() <= DiagnosticMaxBytes {
				return nil
			}
			return writer.rotate(file)
		})
	})
	if err != nil {
		return nil, wrapError("prepare diagnostic logs", errors.Join(err, closeFile(directory)))
	}
	return writer, nil
}

func (writer *DiagnosticWriter) locked(action func() error) error {
	if writer.directory == nil {
		return wrapError("lock diagnostic log", os.ErrClosed)
	}
	return withLockedFile(writer.directory, DiagnosticName+".lock", unix.O_RDWR|unix.O_CREAT, func(_ *os.File) error {
		return action()
	})
}

// Write appends a record after rotating a full diagnostic log.
func (writer *DiagnosticWriter) Write(data []byte) (count int, result error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(data) > DiagnosticMaxBytes {
		return 0, wrapError("write diagnostic record", errors.New("record exceeds diagnostic size limit"))
	}
	result = writer.locked(func() error {
		return withLockedFile(writer.directory, DiagnosticName, unix.O_RDWR|unix.O_CREAT, func(file *os.File) error {
			info, err := file.Stat()
			if err != nil {
				return wrapError("stat diagnostic log before append", err)
			}
			if info.Size()+int64(len(data)) > DiagnosticMaxBytes {
				if err := writer.rotate(file); err != nil {
					return err
				}
			}
			if _, err := file.Seek(0, io.SeekEnd); err != nil {
				return wrapError("seek diagnostic append position", err)
			}
			count, err = file.Write(data)
			return wrapError("append diagnostic record", err)
		})
	})
	return count, result
}

// Close releases the diagnostic directory descriptor.
func (writer *DiagnosticWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.directory == nil {
		return nil
	}
	err := writer.directory.Close()
	writer.directory = nil
	return wrapError("close diagnostic directory", err)
}

func (writer *DiagnosticWriter) rotate(file *os.File) error {
	data, err := readTail(file, DiagnosticMaxBytes)
	if err != nil {
		return err
	}
	if err := writer.compress(bytes.NewReader(data)); err != nil {
		return err
	}
	return wrapError("truncate rotated diagnostic log", file.Truncate(0))
}

func (writer *DiagnosticWriter) compress(source io.Reader) (result error) {
	if file, err := openFile(writer.directory, DiagnosticArchiveName, unix.O_RDONLY); err == nil {
		if err := closeFile(file); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	name := ".agent-gate-" + rand.Text() + ".tmp"
	file, err := openFile(writer.directory, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer func() {
		removeErr := unix.Unlinkat(int(writer.directory.Fd()), name, 0)
		if !errors.Is(removeErr, unix.ENOENT) {
			result = errors.Join(result, wrapError("remove temporary diagnostic archive", removeErr))
		}
	}()
	compressed := gzip.NewWriter(file)
	_, copyErr := io.Copy(compressed, io.LimitReader(source, DiagnosticMaxBytes))
	result = wrapError("compress diagnostic archive", errors.Join(copyErr, compressed.Close(), closeFile(file)))
	if result != nil {
		return result
	}
	return wrapError("replace diagnostic archive", unix.Renameat(int(writer.directory.Fd()), name, int(writer.directory.Fd()), DiagnosticArchiveName))
}
