package auditstorage

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maximumSegmentBytes int64 = 64 * 1024 * 1024
	minimumSegmentBytes int64 = 1024 * 1024
)

// SegmentBytes returns the file size that triggers rotation within a UTC window.
func (policy RotationPolicy) SegmentBytes() int64 {
	if policy.MaxBytes == 0 {
		return 0
	}
	return max(minimumSegmentBytes, min(maximumSegmentBytes, policy.MaxBytes/16))
}

// NeedsRotation checks file sizes without opening SQLite or changing the catalog.
func (catalog *Catalog) NeedsRotation(bucket Bucket, policy RotationPolicy, now time.Time) (bool, error) {
	if !bucket.Start.Equal(bucketStart(now, policy.Interval)) {
		return true, nil
	}
	if policy.MaxBytes == 0 {
		return false, nil
	}
	size, err := bucketBytes(bucket.Path)
	if err != nil {
		return false, err
	}
	return size >= policy.SegmentBytes(), nil
}

func (catalog *Catalog) selectCurrent(policy RotationPolicy, now time.Time, state CatalogState) (Bucket, uint64, error) {
	current := catalog.current(policy, now)
	buckets, err := discover(catalog.options.BasePath)
	if err != nil {
		return Bucket{}, 0, err
	}
	sequence := state.SegmentSequence
	for _, bucket := range buckets {
		_, suffix, segmented := strings.Cut(bucket.ID, "-")
		if segmented {
			value, err := strconv.ParseUint(suffix, 10, 64)
			if err != nil {
				return Bucket{}, 0, storageError("read segment sequence", err)
			}
			sequence = max(sequence, value)
		}
		if bucket.Start.Equal(current.Start) {
			current = bucket
		}
	}
	rotate, err := catalog.NeedsRotation(current, policy, now)
	if err != nil {
		return Bucket{}, 0, err
	}
	if !rotate {
		return current, sequence, nil
	}
	if sequence == math.MaxUint64 {
		return Bucket{}, 0, errors.New("audit segment sequence exhausted")
	}
	sequence++
	current.ID = fmt.Sprintf("%s-%020d", current.Start.Format(bucketTimeFormat), sequence)
	current.Path = catalog.segmentPath(current.ID)
	return current, sequence, nil
}

func (catalog *Catalog) segmentPath(id string) string {
	extension := filepath.Ext(catalog.options.BasePath)
	return strings.TrimSuffix(catalog.options.BasePath, extension) + "-" + id + extension
}

func bucketBytes(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, storageError("stat audit storage", err)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("audit storage %s must be a regular file", path+suffix)
		}
		if info.Size() > math.MaxInt64-total {
			return 0, errors.New("audit storage size overflows bytes")
		}
		total += info.Size()
	}
	return total, nil
}
