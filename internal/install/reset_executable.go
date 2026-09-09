package installer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func resetAttributes(path string) (map[string][]byte, error) {
	size, err := unix.Listxattr(path, nil)
	if err != nil {
		return nil, resetFailure("reset executable", err)
	}
	buffer := make([]byte, size)
	if size > 0 {
		size, err = unix.Listxattr(path, buffer)
		if err != nil {
			return nil, resetFailure("reset executable", err)
		}
	}
	attributes := make(map[string][]byte)
	for name := range bytes.SplitSeq(buffer[:size], []byte{0}) {
		if len(name) == 0 {
			continue
		}
		length, err := unix.Getxattr(path, string(name), nil)
		if err != nil {
			return nil, resetFailure("reset executable", err)
		}
		value := make([]byte, length)
		length, err = unix.Getxattr(path, string(name), value)
		if err != nil {
			return nil, resetFailure("reset executable", err)
		}
		attributes[string(name)] = value[:length]
	}
	return attributes, nil
}

func copyResetExecutable(ctx context.Context, source string, target string) error {
	slog.DebugContext(ctx, "stage reset executable", "source", source, "target", target)
	input, err := os.Open(source)
	if err != nil {
		return resetFailure("reset executable", err)
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return resetFailure("reset executable", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("reset executable is not a runnable regular file")
	}
	attributes, err := resetAttributes(source)
	if err != nil {
		return resetFailure("reset executable", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return resetFailure("reset executable", err)
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".reset-executable-")
	if err != nil {
		return resetFailure("reset executable", err)
	}
	defer func() { _ = os.Remove(output.Name()) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hash), input); err != nil {
		_ = output.Close()
		return resetFailure("reset executable", err)
	}
	if err := output.Chmod(info.Mode()); err != nil {
		_ = output.Close()
		return resetFailure("reset executable", err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return resetFailure("reset executable", err)
	}
	if err := output.Close(); err != nil {
		return resetFailure("reset executable", err)
	}
	for name, value := range attributes {
		if err := unix.Setxattr(output.Name(), name, value, 0); err != nil {
			return resetFailure("reset executable", err)
		}
	}
	if err := verifyResetExecutable(ctx, output.Name(), hash.Sum(nil), info.Mode(), attributes); err != nil {
		return resetFailure("reset executable", err)
	}
	if err := os.Rename(output.Name(), target); err != nil {
		return resetFailure("restore reset executable", err)
	}
	return nil
}

func verifyResetExecutable(ctx context.Context, path string, digest []byte, mode os.FileMode, attributes map[string][]byte) error {
	file, err := os.Open(path)
	if err != nil {
		return resetFailure("reset executable", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return resetFailure("reset executable", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return resetFailure("reset executable", err)
	}
	if !bytes.Equal(hash.Sum(nil), digest) || info.Mode() != mode {
		return errors.New("staged executable bytes or mode differ")
	}
	actual, err := resetAttributes(path)
	if err != nil {
		return resetFailure("reset executable", err)
	}
	for name, value := range attributes {
		if !bytes.Equal(value, actual[name]) {
			return fmt.Errorf("staged executable attribute %s differs", name)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, path, "version").CombinedOutput(); err != nil {
		slog.WarnContext(ctx, "staged executable failed", "err", err)
		return fmt.Errorf("run staged executable: %w: %s", err, output)
	}
	return nil
}
