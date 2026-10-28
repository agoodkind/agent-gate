package logstorage

import (
	"bytes"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Maintain keeps supervisor output and fail-open evidence within their size limits.
func Maintain(stateDir string) (result error) {
	directory, err := openDirectory(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeFile(directory)) }()
	for _, entry := range []struct {
		name  string
		prune func(*os.File) error
	}{
		{SupervisorName, func(file *os.File) error { return keepTail(file, SupervisorMaxBytes) }},
		{FailOpenName, func(file *os.File) error { return pruneFailOpen(directory, file) }},
	} {
		err := withLockedFile(directory, entry.name, unix.O_RDWR, entry.prune)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, logError("maintain "+entry.name, err))
		}
	}
	return result
}

func readTail(file *os.File, limit int64) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, logError("stat log tail", err)
	}
	start := max(int64(0), info.Size()-limit)
	data := make([]byte, info.Size()-start)
	count, err := file.ReadAt(data, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, logError("read log tail", err)
	}
	data = data[:count]
	if start > 0 {
		if newline := bytes.IndexByte(data, '\n'); newline >= 0 && newline+1 < len(data) {
			data = data[newline+1:]
		}
	}
	return data, nil
}

func keepTail(file *os.File, limit int64) error {
	info, err := file.Stat()
	if err != nil {
		return logError("stat supervisor log", err)
	}
	if info.Size() <= limit {
		return nil
	}
	data, err := readTail(file, limit)
	if err != nil {
		return err
	}
	if _, err := file.WriteAt(data, 0); err != nil {
		return logError("write retained supervisor output", err)
	}
	return logError("truncate supervisor output", file.Truncate(int64(len(data))))
}

func markFailOpenTruncated(directory *os.File) error {
	file, err := openFile(directory, FailOpenTruncatedName, unix.O_CREAT|unix.O_WRONLY)
	if err != nil {
		return err
	}
	return closeFile(file)
}

func pruneFailOpen(directory *os.File, file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return logError("stat fail-open evidence", err)
	}
	if info.Size() <= FailOpenMaxBytes {
		return nil
	}
	if err := markFailOpenTruncated(directory); err != nil {
		return err
	}
	data := make([]byte, FailOpenMaxBytes)
	count, err := file.ReadAt(data, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return logError("read earliest fail-open evidence", err)
	}
	end := bytes.LastIndexByte(data[:count], '\n') + 1
	return logError("truncate fail-open evidence", file.Truncate(int64(end)))
}

// AppendFailOpen writes one record under the same file lock that Maintain uses.
func AppendFailOpen(stateDir string, line []byte) (result error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return logError("create fail-open directory", err)
	}
	directory, err := openDirectory(stateDir)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeFile(directory)) }()
	return withLockedFile(directory, FailOpenName, unix.O_RDWR|unix.O_CREAT, func(file *os.File) error {
		if err := pruneFailOpen(directory, file); err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			return logError("stat fail-open evidence before append", err)
		}
		if info.Size()+int64(len(line)) > FailOpenMaxBytes {
			return markFailOpenTruncated(directory)
		}
		_, err = file.WriteAt(line, info.Size())
		return logError("append fail-open evidence", err)
	})
}

// ReadFailOpen reads a bounded snapshot after pruning oversized evidence.
func ReadFailOpen(stateDir string) (data []byte, result error) {
	directory, err := openDirectory(stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { result = errors.Join(result, closeFile(directory)) }()
	result = withLockedFile(directory, FailOpenName, unix.O_RDWR, func(file *os.File) error {
		if err := pruneFailOpen(directory, file); err != nil {
			return err
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(file, FailOpenMaxBytes))
		return logError("read retained fail-open evidence", err)
	})
	return data, result
}
