// Package logstorage bounds diagnostic, supervisor, and fail-open log files.
package logstorage

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

const (
	// DiagnosticName identifies the active diagnostic log.
	DiagnosticName = "agent-gate.jsonl"
	// SupervisorName identifies supervisor stdout and stderr output.
	SupervisorName = "agent-gate.log"
	// FailOpenName identifies evidence of unevaluated calls.
	FailOpenName = "fail-open.jsonl"
	// FailOpenTruncatedName identifies the marker for incomplete evidence.
	FailOpenTruncatedName = "fail-open.truncated"
	// DiagnosticMaxBytes limits the active diagnostic log.
	DiagnosticMaxBytes = 1024 * 1024
	// SupervisorMaxBytes limits supervisor output after maintenance.
	SupervisorMaxBytes = 1024 * 1024
	// FailOpenMaxBytes limits retained fail-open evidence.
	FailOpenMaxBytes = 2 * 1024 * 1024
)

// File errors use stderr independently of the diagnostic writer.
var fileErrorLog = slog.New(slog.NewJSONHandler(os.Stderr, nil))

func logError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		fileErrorLog.Warn(operation+" failed", "err", err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func closeFile(file *os.File) error {
	return logError("close "+file.Name(), file.Close())
}

func openDirectory(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, logError("open log directory", err)
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func openFile(directory *os.File, name string, flags int) (*os.File, error) {
	descriptor, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, logError("open "+name, err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	var status unix.Stat_t
	if err := unix.Fstat(descriptor, &status); err != nil {
		return nil, logError("stat "+name, errors.Join(err, closeFile(file)))
	}
	if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Nlink != 1 {
		return nil, logError("validate "+name, errors.Join(errors.New("requires a regular file without hard links"), closeFile(file)))
	}
	return file, nil
}

func withLockedFile(directory *os.File, name string, flags int, action func(*os.File) error) (result error) {
	file, err := openFile(directory, name, flags)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeFile(file)) }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		return logError("lock "+name, err)
	}
	defer func() {
		result = errors.Join(result, logError("unlock "+name, unix.Flock(int(file.Fd()), unix.LOCK_UN)))
	}()
	return action(file)
}
