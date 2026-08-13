//go:build darwin || linux

package auditstorage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"syscall"
)

// ReadFileIdentity returns the content and filesystem identity used by cutover journals.
func ReadFileIdentity(path string) (FileIdentity, error) {
	// #nosec G304 -- callers supply the configured database or its unique sibling.
	file, err := os.Open(path)
	if err != nil {
		return FileIdentity{}, reportCutoverError("open cutover identity file", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return FileIdentity{}, reportCutoverError("hash cutover identity file", err)
	}
	info, err := file.Stat()
	if err != nil {
		return FileIdentity{}, reportCutoverError("stat cutover identity file", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, errors.New("cutover file identity is unavailable")
	}
	return FileIdentity{
		Device: formatCutoverDevice(stat),
		Inode:  strconv.FormatUint(stat.Ino, 10),
		Size:   info.Size(),
		SHA256: hex.EncodeToString(hash.Sum(nil)),
	}, nil
}

func guardCommittedCutoverIdentity(path string, expected FileIdentity) error {
	if expected.Device == "" || expected.Inode == "" || expected.SHA256 == "" {
		return errors.New("committed compact audit database identity is missing")
	}
	actual, err := ReadFileIdentity(path)
	if err != nil {
		return reportCutoverError("read committed compact audit database identity", err)
	}
	if actual != expected {
		return errors.New("committed compact audit database identity does not match journal")
	}
	return nil
}

func guardCommittedCutoverFilesystemIdentity(path string, expected FileIdentity) error {
	if expected.Device == "" || expected.Inode == "" {
		return errors.New("committed compact audit database filesystem identity is missing")
	}
	info, err := os.Stat(path)
	if err != nil {
		return reportCutoverError("stat committed compact audit database identity", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cutover file identity is unavailable")
	}
	if formatCutoverDevice(stat) != expected.Device ||
		strconv.FormatUint(stat.Ino, 10) != expected.Inode {
		return errors.New("committed compact audit database filesystem identity does not match journal")
	}
	return nil
}
