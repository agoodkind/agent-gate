package auditstorage

import (
	"errors"
	"os"
	"time"
)

// BucketStatus reports file sizes without opening SQLite.
type BucketStatus struct {
	ID            string `json:"bucket_id"`
	Path          string `json:"path"`
	DatabaseBytes int64  `json:"database_bytes"`
	WALBytes      int64  `json:"wal_bytes"`
	SHMBytes      int64  `json:"shm_bytes"`
}

// AuditStatus reports the normalized policy and retained storage footprint.
type AuditStatus struct {
	BucketInterval    string         `json:"bucket_interval"`
	RetentionBuckets  int            `json:"retention_buckets"`
	CurrentBucketID   string         `json:"current_bucket_id"`
	CurrentBucketPath string         `json:"current_bucket_path"`
	RetainedFiles     []BucketStatus `json:"retained_files"`
	TotalBytes        int64          `json:"total_bytes"`
	NextBoundary      time.Time      `json:"next_boundary"`
	ResetPending      bool           `json:"reset_pending"`
	CleanupError      string         `json:"cleanup_error,omitempty"`
}

// Status uses only catalog metadata and file stats, even while writers are active.
func (catalog *Catalog) Status(policy RotationPolicy, now time.Time) (AuditStatus, error) {
	if err := policy.Validate(); err != nil {
		return AuditStatus{}, err
	}
	current := catalog.current(policy, now)
	status := AuditStatus{
		BucketInterval: policy.Interval.String(), RetentionBuckets: policy.Retained,
		CurrentBucketID: current.ID, CurrentBucketPath: current.Path,
		RetainedFiles: make([]BucketStatus, 0), NextBoundary: current.Start.Add(policy.Interval), TotalBytes: 0, ResetPending: false, CleanupError: "",
	}
	state, err := catalog.checkState(policy)
	status.ResetPending = state.ResetPending
	status.CleanupError = state.CleanupError
	if err != nil {
		status.CleanupError = err.Error()
	}
	buckets, err := discover(catalog.options.BasePath)
	if err != nil {
		return status, storageError("read bucket status", err)
	}
	for _, bucket := range buckets {
		if !retained(bucket, policy, now) {
			continue
		}
		entry := BucketStatus{ID: bucket.ID, Path: bucket.Path, DatabaseBytes: 0, WALBytes: 0, SHMBytes: 0}
		for _, file := range []struct {
			suffix string
			size   *int64
		}{
			{"", &entry.DatabaseBytes}, {"-wal", &entry.WALBytes}, {"-shm", &entry.SHMBytes},
		} {
			info, err := os.Stat(bucket.Path + file.suffix)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return status, storageError("stat retained bucket", err)
			}
			*file.size = info.Size()
		}
		status.TotalBytes += entry.DatabaseBytes + entry.WALBytes + entry.SHMBytes
		status.RetainedFiles = append(status.RetainedFiles, entry)
	}
	return status, nil
}
