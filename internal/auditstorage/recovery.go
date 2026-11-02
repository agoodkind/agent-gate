package auditstorage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// RecoveryFiles streams one directory without retaining a file list.
type RecoveryFiles struct {
	directory       *os.File
	base            string
	policy          RotationPolicy
	at              time.Time
	maximumSequence uint64
}

// Recovery opens a bounded directory iterator without opening databases.
func (catalog *Catalog) Recovery(ctx context.Context, policy RotationPolicy, at time.Time) (*RecoveryFiles, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.checkStateSummary(ctx, policy)
	if err != nil {
		return nil, err
	}
	directory, err := os.Open(filepath.Dir(catalog.options.BasePath))
	if err != nil {
		return nil, storageError("open recovery directory", err)
	}
	maximum, err := capturePublishedSequence(ctx, directory, catalog.options.BasePath, state.SegmentSequence)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	return &RecoveryFiles{directory: directory, base: catalog.options.BasePath, policy: policy, at: at, maximumSequence: maximum}, nil
}

func capturePublishedSequence(ctx context.Context, directory *os.File, base string, maximum uint64) (uint64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, storageError("capture recovery scope", err)
		}
		entries, err := directory.ReadDir(100)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, storageError("capture published segments", err)
		}
		for _, entry := range entries {
			bucket, valid := parseBucket(base, entry.Name())
			if valid {
				maximum = max(maximum, recoverySequence(bucket))
			}
		}
	}
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return 0, storageError("reset recovery directory", err)
	}
	return maximum, nil
}

func recoverySequence(bucket Bucket) uint64 {
	_, suffix, segmented := strings.Cut(bucket.ID, "-")
	if !segmented {
		return 0
	}
	sequence, _ := strconv.ParseUint(suffix, 10, 64)
	return sequence
}

// Next examines at most 100 directory entries per call. An empty page can
// precede more files. [io.EOF] ends the captured directory traversal.
func (files *RecoveryFiles) Next() ([]Bucket, error) {
	entries, err := files.directory.ReadDir(100)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, storageError("read recovery directory", err)
	}
	buckets := make([]Bucket, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		bucket, valid := parseBucket(files.base, entry.Name())
		if valid && recoverySequence(bucket) <= files.maximumSequence && retained(bucket, files.policy, files.at) {
			buckets = append(buckets, bucket)
		}
	}
	if errors.Is(err, io.EOF) {
		return buckets, io.EOF
	}
	return buckets, nil
}

// Close releases the directory descriptor.
func (files *RecoveryFiles) Close() error {
	if err := files.directory.Close(); err != nil {
		return storageError("close recovery directory", err)
	}
	return nil
}
