package auditstorage

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

// BucketStatus reports file sizes without opening SQLite.
type BucketStatus struct {
	ID            string `json:"bucket_id"`
	Path          string `json:"path"`
	DatabaseBytes int64  `json:"database_bytes"`
	WALBytes      int64  `json:"wal_bytes"`
	SHMBytes      int64  `json:"shm_bytes"`
	JournalBytes  int64  `json:"journal_bytes"`
}

// AuditStatus reports the normalized policy and retained storage footprint.
type AuditStatus struct {
	BucketInterval    string         `json:"bucket_interval"`
	RetentionBuckets  int            `json:"retention_buckets"`
	CurrentBucketID   string         `json:"current_bucket_id"`
	CurrentBucketPath string         `json:"current_bucket_path"`
	RetainedFiles     []BucketStatus `json:"retained_files"`
	TotalBytes        int64          `json:"total_bytes"`
	MaxBytes          int64          `json:"max_bytes"`
	ExcessBytes       int64          `json:"excess_bytes"`
	PendingFiles      []BucketStatus `json:"pending_files"`
	DeferredFiles     []string       `json:"deferred_files"`
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
		MaxBytes: policy.MaxBytes, ExcessBytes: 0, PendingFiles: make([]BucketStatus, 0), DeferredFiles: nil,
	}
	state, err := catalog.checkState(policy)
	status.ResetPending = state.ResetPending
	status.CleanupError = state.CleanupError
	status.MaxBytes = policy.MaxBytes
	status.DeferredFiles = state.DeferredFiles
	status.PendingFiles = make([]BucketStatus, 0)
	activeName := filepath.Base(catalog.segmentPath(state.CurrentBucketID))
	if active, ok := parseBucket(catalog.options.BasePath, activeName); ok {
		status.CurrentBucketID = active.ID
		status.CurrentBucketPath = active.Path
		status.NextBoundary = active.Start.Add(policy.Interval)
	}
	if err != nil {
		status.CleanupError = err.Error()
	}
	files, total, err := catalog.storageFiles(state.PendingBasePaths...)
	if err != nil {
		return status, storageError("read bucket status", err)
	}
	status.TotalBytes = total
	if policy.MaxBytes > 0 {
		status.ExcessBytes = max(int64(0), total-policy.MaxBytes)
	}
	for _, file := range files {
		bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(file.path))
		if !recognized || bucket.Path != file.path {
			recognized = false
			bucket.Path = file.path
		}
		entry := BucketStatus{ID: bucket.ID, Path: bucket.Path, DatabaseBytes: 0, WALBytes: 0, SHMBytes: 0, JournalBytes: 0}
		for _, file := range []struct {
			suffix string
			size   *int64
		}{
			{"", &entry.DatabaseBytes}, {"-wal", &entry.WALBytes}, {"-shm", &entry.SHMBytes}, {"-journal", &entry.JournalBytes},
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
		if recognized && retained(bucket, policy, now) {
			status.RetainedFiles = append(status.RetainedFiles, entry)
		} else {
			status.PendingFiles = append(status.PendingFiles, entry)
		}
	}
	return status, nil
}
