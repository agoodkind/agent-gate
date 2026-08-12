//go:build unix

package auditmaintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	snapshotLockHelperMode = "AGENT_GATE_SNAPSHOT_LOCK_HELPER"
	snapshotLockTestFail   = "AGENT_GATE_SNAPSHOT_LOCK_TEST_FAIL"
	snapshotLockTokenFD    = 3
	snapshotLockSHMFD      = 4
	snapshotLockReadyFD    = 5
	snapshotLockReleaseFD  = 6
	snapshotLockWait       = 5 * time.Second

	// SQLite defines UNIX_SHM_BASE as (22 + SQLITE_SHM_NLOCK) * 4 = 120.
	// WAL_CKPT_LOCK is slot 1, so the Unix checkpoint lock byte is 121.
	sqliteCheckpointLockByte = 121
)

type snapshotLockProcess struct {
	command *exec.Cmd
	release *os.File
	wait    <-chan error
	once    sync.Once
	err     error
}

type snapshotLockPipes struct {
	tokenReader   *os.File
	tokenWriter   *os.File
	readyReader   *os.File
	readyWriter   *os.File
	releaseReader *os.File
	releaseWriter *os.File
}

func init() {
	if os.Getenv(snapshotLockHelperMode) != "1" {
		return
	}
	if !validSnapshotLockHelperInvocation() {
		return
	}
	os.Exit(runSnapshotLockHelper())
}

func holdSnapshotCheckpointLock(ctx context.Context, sharedMemoryPath string) (func() error, error) {
	slog.DebugContext(ctx, "hold audit snapshot checkpoint lock", "path", sharedMemoryPath)
	// #nosec G703 -- the caller supplies the SQLite database path and this exact sibling.
	if _, err := os.Stat(sharedMemoryPath); errors.Is(err, os.ErrNotExist) {
		return func() error { return nil }, nil
	} else if err != nil {
		return nil, wrapError("stat snapshot shared memory", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, wrapError("locate snapshot lock helper", err)
	}
	pipes, err := createSnapshotLockPipes()
	if err != nil {
		return nil, err
	}
	// #nosec G703 -- the caller supplies the SQLite database path and this exact sibling.
	sharedMemory, err := os.OpenFile(sharedMemoryPath, os.O_RDWR, 0)
	if err != nil {
		closeSnapshotLockPipes(
			pipes.tokenReader, pipes.tokenWriter, pipes.readyReader, pipes.readyWriter,
			pipes.releaseReader, pipes.releaseWriter,
		)
		return nil, wrapError("open snapshot shared memory", err)
	}
	command := exec.CommandContext(context.WithoutCancel(ctx), executable)
	command.Env = append(os.Environ(), snapshotLockHelperMode+"=1")
	command.ExtraFiles = []*os.File{
		pipes.tokenReader, sharedMemory, pipes.readyWriter, pipes.releaseReader,
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		closeSnapshotLockPipes(
			pipes.tokenReader, pipes.tokenWriter, sharedMemory,
			pipes.readyReader, pipes.readyWriter,
			pipes.releaseReader, pipes.releaseWriter,
		)
		return nil, wrapError("start snapshot lock helper", err)
	}
	if err := authorizeSnapshotLockHelper(
		command,
		pipes.tokenReader,
		pipes.tokenWriter,
		sharedMemory,
		pipes.readyReader,
		pipes.readyWriter,
		pipes.releaseReader,
		pipes.releaseWriter,
	); err != nil {
		return nil, wrapError("authorize snapshot lock helper", err)
	}
	wait := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "snapshot lock wait panicked", "err", recovered)
			}
		}()
		wait <- command.Wait()
	}()
	ready := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "snapshot lock readiness panicked", "err", recovered)
			}
		}()
		var signal [1]byte
		_, readErr := io.ReadFull(pipes.readyReader, signal[:])
		ready <- readErr
	}()
	defer func() { _ = pipes.readyReader.Close() }()
	timer := time.NewTimer(snapshotLockWait)
	defer timer.Stop()
	select {
	case err := <-ready:
		if err != nil {
			_ = pipes.releaseWriter.Close()
			waitForSnapshotLockHelper(command, wait)
			return nil, reportSnapshotLockHelperError("acquire", err, stderr.String())
		}
	case err := <-wait:
		_ = pipes.releaseWriter.Close()
		return nil, reportSnapshotLockHelperError("acquire", err, stderr.String())
	case <-ctx.Done():
		_ = pipes.releaseWriter.Close()
		terminateSnapshotLockHelper(command, wait)
		return nil, wrapError("acquire snapshot checkpoint lock", ctx.Err())
	case <-timer.C:
		_ = pipes.releaseWriter.Close()
		terminateSnapshotLockHelper(command, wait)
		return nil, errors.New("acquire snapshot checkpoint lock: helper timed out")
	}
	process := &snapshotLockProcess{
		command: command,
		release: pipes.releaseWriter,
		wait:    wait,
		once:    sync.Once{},
		err:     nil,
	}
	return process.close, nil
}

func createSnapshotLockPipes() (snapshotLockPipes, error) {
	pipes := snapshotLockPipes{
		tokenReader: nil, tokenWriter: nil,
		readyReader: nil, readyWriter: nil,
		releaseReader: nil, releaseWriter: nil,
	}
	var err error
	pipes.readyReader, pipes.readyWriter, err = os.Pipe()
	if err != nil {
		return snapshotLockPipes{}, wrapError("create snapshot lock readiness pipe", err)
	}
	pipes.releaseReader, pipes.releaseWriter, err = os.Pipe()
	if err != nil {
		closeSnapshotLockPipes(pipes.readyReader, pipes.readyWriter)
		return snapshotLockPipes{}, wrapError("create snapshot lock release pipe", err)
	}
	pipes.tokenReader, pipes.tokenWriter, err = os.Pipe()
	if err != nil {
		closeSnapshotLockPipes(
			pipes.readyReader, pipes.readyWriter,
			pipes.releaseReader, pipes.releaseWriter,
		)
		return snapshotLockPipes{}, wrapError("create snapshot lock token pipe", err)
	}
	return pipes, nil
}

func authorizeSnapshotLockHelper(
	command *exec.Cmd,
	tokenReader *os.File,
	tokenWriter *os.File,
	sharedMemory *os.File,
	readyReader *os.File,
	readyWriter *os.File,
	releaseReader *os.File,
	releaseWriter *os.File,
) error {
	_ = tokenReader.Close()
	_ = sharedMemory.Close()
	_ = readyWriter.Close()
	_ = releaseReader.Close()
	if _, err := tokenWriter.Write([]byte{1}); err != nil {
		_ = tokenWriter.Close()
		_ = releaseWriter.Close()
		_ = command.Process.Kill()
		_ = command.Wait()
		_ = readyReader.Close()
		return wrapError("write snapshot lock helper token", err)
	}
	if err := tokenWriter.Close(); err != nil {
		return wrapError("close snapshot lock helper token", err)
	}
	return nil
}

func (process *snapshotLockProcess) close() error {
	process.once.Do(func() {
		if _, err := process.release.Write([]byte{1}); err != nil {
			process.err = wrapError("release snapshot checkpoint lock", err)
		}
		_ = process.release.Close()
		timer := time.NewTimer(snapshotLockWait)
		defer timer.Stop()
		select {
		case err := <-process.wait:
			if err != nil && process.err == nil {
				process.err = wrapError("wait for snapshot lock helper", err)
			}
		case <-timer.C:
			_ = process.command.Process.Kill()
			waitForSnapshotLockHelper(process.command, process.wait)
			if process.err == nil {
				process.err = errors.New("release snapshot checkpoint lock: helper timed out")
			}
		}
	})
	return process.err
}

func runSnapshotLockHelper() int {
	slog.Debug("run audit snapshot checkpoint lock helper")
	if os.Getenv(snapshotLockTestFail) == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "snapshot lock helper test failure")
		return 1
	}
	sharedMemory := os.NewFile(snapshotLockSHMFD, "snapshot-lock-shared-memory")
	if sharedMemory == nil {
		_, _ = fmt.Fprintln(os.Stderr, "shared memory descriptor unavailable")
		return 1
	}
	defer func() { _ = sharedMemory.Close() }()
	lock := unix.Flock_t{
		Type: unix.F_WRLCK, Whence: io.SeekStart,
		Start: sqliteCheckpointLockByte, Len: 1, Pid: 0,
	}
	if err := unix.FcntlFlock(sharedMemory.Fd(), unix.F_SETLKW, &lock); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "lock checkpoint byte: %v\n", err)
		return 1
	}
	defer func() {
		lock.Type = unix.F_UNLCK
		_ = unix.FcntlFlock(sharedMemory.Fd(), unix.F_SETLK, &lock)
	}()
	ready := os.NewFile(snapshotLockReadyFD, "snapshot-lock-ready")
	release := os.NewFile(snapshotLockReleaseFD, "snapshot-lock-release")
	if ready == nil || release == nil {
		_, _ = fmt.Fprintln(os.Stderr, "snapshot lock helper pipes unavailable")
		return 1
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "signal checkpoint lock readiness: %v\n", err)
		return 1
	}
	_ = ready.Close()
	var signal [1]byte
	if _, err := release.Read(signal[:]); err != nil && !errors.Is(err, io.EOF) {
		_, _ = fmt.Fprintf(os.Stderr, "wait for checkpoint lock release: %v\n", err)
		return 1
	}
	_ = release.Close()
	return 0
}

func validSnapshotLockHelperInvocation() bool {
	for _, descriptor := range []int{
		snapshotLockTokenFD,
		snapshotLockSHMFD,
		snapshotLockReadyFD,
		snapshotLockReleaseFD,
	} {
		if _, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0); err != nil {
			return false
		}
	}
	token := os.NewFile(snapshotLockTokenFD, "snapshot-lock-token")
	if token == nil {
		return false
	}
	var signal [1]byte
	_, err := io.ReadFull(token, signal[:])
	_ = token.Close()
	return err == nil && signal[0] == 1
}

func closeSnapshotLockPipes(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func reportSnapshotLockHelperError(action string, err error, stderr string) error {
	var result error
	if message := string(bytes.TrimSpace([]byte(stderr))); message != "" {
		result = fmt.Errorf("%s snapshot checkpoint lock: %w: %s", action, err, message)
	} else {
		result = fmt.Errorf("%s snapshot checkpoint lock: %w", action, err)
	}
	slog.Warn("snapshot checkpoint lock helper failed", "action", action, "err", result)
	return result
}

func terminateSnapshotLockHelper(command *exec.Cmd, wait <-chan error) {
	_ = command.Process.Kill()
	waitForSnapshotLockHelper(command, wait)
}

func waitForSnapshotLockHelper(command *exec.Cmd, wait <-chan error) {
	select {
	case <-wait:
	case <-time.After(snapshotLockWait):
		_ = command.Process.Kill()
		<-wait
	}
}
