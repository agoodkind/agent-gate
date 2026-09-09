package auditstorage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const bucketTimeFormat = "20060102T150405Z"

// ErrPolicyChanged requires a destructive reset before opening another policy.
var ErrPolicyChanged = errors.New("audit storage policy changed")

// ErrResetPending requires completing the recorded deletion before opening data.
var ErrResetPending = errors.New("audit storage reset pending")

// RotationPolicy selects UTC windows and counts the current window in retention.
type RotationPolicy struct {
	Interval time.Duration
	Retained int
}

// Bucket identifies one physical UTC window.
type Bucket struct {
	ID    string
	Path  string
	Start time.Time
}

// CatalogOptions separates storage identity from stable coordination and state.
type CatalogOptions struct {
	BasePath         string
	StatePath        string
	CoordinationPath string
}

// Catalog coordinates file selection independently of the database stores.
type Catalog struct {
	options              CatalogOptions
	filesystemCheckpoint func(operation string, path string) error
}

// PruneResult distinguishes removed databases from databases still in use.
type PruneResult struct {
	Removed  []string
	Deferred []string
}

// NewCatalog canonicalizes paths without opening history.
func NewCatalog(options CatalogOptions) (*Catalog, error) {
	if err := regularOrAbsent(options.BasePath); err != nil {
		return nil, err
	}
	for _, path := range []*string{&options.BasePath, &options.StatePath, &options.CoordinationPath} {
		canonical, err := canonicalPath(*path)
		if err != nil {
			return nil, err
		}
		*path = canonical
	}
	if options.BasePath == options.StatePath || options.BasePath == options.CoordinationPath || options.StatePath == options.CoordinationPath {
		return nil, errors.New("catalog paths must be distinct")
	}
	return &Catalog{options: options, filesystemCheckpoint: nil}, nil
}

func bucketStart(now time.Time, interval time.Duration) time.Time {
	seconds := int64(interval / time.Second)
	value := now.Unix()
	remainder := value % seconds
	if remainder < 0 {
		remainder += seconds
	}
	return time.Unix(value-remainder, 0).UTC()
}

// Validate rejects policies whose retained span cannot fit in a duration.
func (policy RotationPolicy) Validate() error {
	if policy.Interval <= 0 || policy.Interval%time.Second != 0 {
		return errors.New("bucket interval must be positive whole seconds")
	}
	if policy.Retained <= 0 {
		return errors.New("retention buckets must be positive")
	}
	if int64(policy.Retained) > math.MaxInt64/int64(policy.Interval) {
		return errors.New("retained bucket interval overflows duration")
	}
	return nil
}

func (catalog *Catalog) desiredState(policy RotationPolicy) CatalogState {
	return CatalogState{BasePath: catalog.options.BasePath, IntervalSeconds: int64(policy.Interval / time.Second), Retained: policy.Retained, ResetPending: false, PendingBasePaths: nil}
}

func (catalog *Catalog) checkState(policy RotationPolicy) (CatalogState, error) {
	if err := policy.Validate(); err != nil {
		return CatalogState{}, err
	}
	state, err := catalog.loadState()
	if err != nil {
		return state, err
	}
	if state.ResetPending {
		return state, ErrResetPending
	}
	if state.BasePath != "" && !sameIdentity(state, catalog.desiredState(policy)) {
		return state, ErrPolicyChanged
	}
	return state, nil
}

func sameIdentity(left CatalogState, right CatalogState) bool {
	return left.BasePath == right.BasePath && left.IntervalSeconds == right.IntervalSeconds && left.Retained == right.Retained
}

func (catalog *Catalog) current(policy RotationPolicy, now time.Time) Bucket {
	start := bucketStart(now, policy.Interval)
	id := start.Format(bucketTimeFormat)
	extension := filepath.Ext(catalog.options.BasePath)
	path := strings.TrimSuffix(catalog.options.BasePath, extension) + "-" + id + extension
	return Bucket{ID: id, Path: path, Start: start}
}

// EnsureCurrent selects or initializes the current database only for the saved policy.
func (catalog *Catalog) EnsureCurrent(ctx context.Context, policy RotationPolicy, now time.Time) (Bucket, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return Bucket{}, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.checkState(policy)
	if err != nil {
		return Bucket{}, err
	}
	if err := catalog.cleanStaging(ctx); err != nil {
		return Bucket{}, err
	}
	bucket := catalog.current(policy, now)
	if err := catalog.initialize(ctx, bucket); err != nil {
		return Bucket{}, err
	}
	if state.BasePath == "" {
		if err := catalog.saveState(catalog.desiredState(policy)); err != nil {
			return Bucket{}, err
		}
	}
	return bucket, nil
}

func (catalog *Catalog) initialize(ctx context.Context, bucket Bucket) error {
	if err := catalog.checkpoint("create", bucket.Path); err != nil {
		return err
	}
	if err := regularOrAbsent(bucket.Path); err != nil {
		return err
	}
	release, err := catalog.pin(bucket.Path)
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	_, statErr := os.Lstat(bucket.Path)
	if errors.Is(statErr, os.ErrNotExist) {
		return catalog.publishNew(ctx, bucket)
	}
	if statErr != nil {
		return storageError("stat current bucket", statErr)
	}
	database, err := OpenWriter(ctx, bucket.Path)
	if err != nil {
		return err
	}
	if err := database.Close(); err != nil {
		return storageError("close initialized bucket", err)
	}
	return syncDirectory(filepath.Dir(bucket.Path))
}

func (catalog *Catalog) publishNew(ctx context.Context, bucket Bucket) error {
	slog.DebugContext(ctx, "initialize new audit bucket", "path", bucket.Path)
	staging := bucket.Path + ".creating"
	removed, err := catalog.removeBucket(ctx, staging)
	if err != nil {
		return err
	}
	if !removed {
		return errors.New("staging bucket is in use")
	}
	if err := regularOrAbsent(staging); err != nil {
		return err
	}
	database, err := OpenWriter(ctx, staging)
	if err != nil {
		return err
	}
	if err := database.Close(); err != nil {
		return storageError("close new audit bucket", err)
	}
	file, err := os.Open(staging)
	if err != nil {
		return storageError("open new bucket for sync", err)
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return storageError("sync new audit bucket", err)
	}
	if err := catalog.checkpoint("publish", bucket.Path); err != nil {
		return err
	}
	if err := os.Rename(staging, bucket.Path); err != nil {
		return storageError("publish new audit bucket", err)
	}
	return syncDirectory(filepath.Dir(bucket.Path))
}

// Reset invalidates all affected families durably before replacing the current database.
func (catalog *Catalog) Reset(ctx context.Context, policy RotationPolicy, now time.Time) (Bucket, error) {
	if err := policy.Validate(); err != nil {
		return Bucket{}, err
	}
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return Bucket{}, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.loadState()
	if err != nil {
		return Bucket{}, err
	}
	pending := catalog.desiredState(policy)
	pending.ResetPending = true
	pending.PendingBasePaths = pendingFamilies(state, catalog.options.BasePath)
	if err := catalog.saveState(pending); err != nil {
		return Bucket{}, err
	}
	if err := catalog.deleteFamilies(ctx, pending.PendingBasePaths); err != nil {
		return Bucket{}, err
	}
	bucket := catalog.current(policy, now)
	if err := catalog.initialize(ctx, bucket); err != nil {
		return Bucket{}, err
	}
	if err := catalog.saveState(catalog.desiredState(policy)); err != nil {
		return Bucket{}, err
	}
	return bucket, nil
}

// Purge records invalidation and deletes the family without creating a replacement.
func (catalog *Catalog) Purge(ctx context.Context) error {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.loadState()
	if err != nil {
		return err
	}
	state.PendingBasePaths = pendingFamilies(state, catalog.options.BasePath)
	if state.BasePath == "" {
		state.BasePath = catalog.options.BasePath
	}
	state.ResetPending = true
	if err := catalog.saveState(state); err != nil {
		return err
	}
	return catalog.deleteFamilies(ctx, state.PendingBasePaths)
}

// OpenWriter pins an existing bucket and refuses handles after invalidation.
func (catalog *Catalog) OpenWriter(ctx context.Context, bucket Bucket) (*BucketHandle, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.loadState()
	if err != nil {
		return nil, err
	}
	if state.ResetPending {
		return nil, ErrResetPending
	}
	if state.BasePath != catalog.options.BasePath {
		return nil, ErrPolicyChanged
	}
	matched, ok := parseBucket(catalog.options.BasePath, filepath.Base(bucket.Path))
	if !ok || matched.Path != bucket.Path || matched.ID != bucket.ID || !matched.Start.Equal(bucket.Start) || state.IntervalSeconds <= 0 || matched.Start.Unix()%state.IntervalSeconds != 0 {
		return nil, errors.New("bucket does not match catalog")
	}
	return catalog.open(ctx, bucket, false)
}

// Read pins existing retained windows and opens SQLite in read-only mode.
func (catalog *Catalog) Read(ctx context.Context, policy RotationPolicy, now time.Time) (*ReadSet, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.checkState(policy)
	if err != nil {
		return nil, err
	}
	set := &ReadSet{Handles: nil}
	if state.BasePath == "" {
		return set, nil
	}
	buckets, err := discover(catalog.options.BasePath)
	if err != nil {
		return nil, err
	}
	for _, bucket := range buckets {
		if !retained(bucket, policy, now) {
			continue
		}
		handle, err := catalog.open(ctx, bucket, true)
		if err != nil {
			return nil, storageError("open retained bucket", errors.Join(err, set.Close()))
		}
		set.Handles = append(set.Handles, handle)
	}
	return set, nil
}

func (catalog *Catalog) open(ctx context.Context, bucket Bucket, readonly bool) (*BucketHandle, error) {
	info, err := os.Lstat(bucket.Path)
	if err != nil {
		return nil, storageError("stat bucket", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("bucket must be a regular file")
	}
	release, err := catalog.pin(bucket.Path)
	if err != nil {
		return nil, err
	}
	var database *sql.DB
	if readonly {
		location := url.URL{Scheme: "file", Path: bucket.Path}
		location.RawQuery = url.Values{"mode": {"ro"}, "_foreign_keys": {"on"}, "_busy_timeout": {"5000"}}.Encode()
		database, err = sql.Open("sqlite3", location.String())
		if err == nil {
			database.SetMaxOpenConns(1)
			database.SetMaxIdleConns(1)
			err = database.PingContext(ctx)
		}
	} else {
		database, err = OpenWriter(ctx, bucket.Path)
	}
	if err != nil {
		if database != nil {
			_ = database.Close()
		}
		return nil, storageError("open bucket", errors.Join(err, release()))
	}
	return &BucketHandle{Bucket: bucket, Database: database, release: release, once: sync.Once{}, closeError: nil}, nil
}

func retained(bucket Bucket, policy RotationPolicy, now time.Time) bool {
	current := bucketStart(now, policy.Interval)
	oldest := current.Add(-time.Duration(policy.Retained-1) * policy.Interval)
	return bucket.Start.Unix()%int64(policy.Interval/time.Second) == 0 && !bucket.Start.Before(oldest) && !bucket.Start.After(current)
}

// Prune deletes expired windows, deferring any that still have a reader or writer.
func (catalog *Catalog) Prune(ctx context.Context, policy RotationPolicy, now time.Time) (PruneResult, error) {
	var result PruneResult
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Close() }()
	if _, err := catalog.checkState(policy); err != nil {
		return result, err
	}
	if err := catalog.cleanStaging(ctx); err != nil {
		return result, err
	}
	paths, err := familyPaths(catalog.options.BasePath)
	if err != nil {
		return result, err
	}
	slices.Sort(paths)
	for _, path := range slices.Compact(paths) {
		bucket, ok := parseBucket(catalog.options.BasePath, filepath.Base(path))
		if !ok {
			continue
		}
		if retained(bucket, policy, now) {
			continue
		}
		removed, err := catalog.removeBucket(ctx, bucket.Path)
		if err != nil {
			return result, err
		}
		if removed {
			result.Removed = append(result.Removed, bucket.Path)
		} else {
			result.Deferred = append(result.Deferred, bucket.Path)
		}
	}
	return result, nil
}

func (catalog *Catalog) cleanStaging(ctx context.Context) error {
	paths, err := familyPaths(catalog.options.BasePath)
	if err != nil {
		return err
	}
	slices.Sort(paths)
	for _, path := range slices.Compact(paths) {
		if path == catalog.options.BasePath || !strings.HasSuffix(path, ".creating") {
			continue
		}
		if _, primary := parseBucket(catalog.options.BasePath, filepath.Base(path)); primary {
			continue
		}
		removed, err := catalog.removeBucket(ctx, path)
		if err != nil {
			return err
		}
		if !removed {
			return errors.New("staging bucket is in use")
		}
	}
	return nil
}

func discover(base string) ([]Bucket, error) {
	entries, err := os.ReadDir(filepath.Dir(base))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError("discover buckets", err)
	}
	var buckets []Bucket
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		bucket, ok := parseBucket(base, entry.Name())
		if ok {
			buckets = append(buckets, bucket)
		}
	}
	slices.SortFunc(buckets, func(a Bucket, b Bucket) int { return strings.Compare(a.Path, b.Path) })
	return buckets, nil
}

func parseBucket(base string, name string) (Bucket, bool) {
	extension := filepath.Ext(base)
	prefix := strings.TrimSuffix(filepath.Base(base), extension) + "-"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, extension) {
		return Bucket{ID: "", Path: "", Start: time.Time{}}, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), extension)
	start, err := time.Parse(bucketTimeFormat, id)
	if err != nil || start.Format(bucketTimeFormat) != id {
		return Bucket{ID: "", Path: "", Start: time.Time{}}, false
	}
	return Bucket{ID: id, Path: filepath.Join(filepath.Dir(base), name), Start: start}, true
}

func (catalog *Catalog) deleteFamilies(ctx context.Context, bases []string) error {
	var paths []string
	for _, base := range bases {
		members, err := familyPaths(base)
		if err != nil {
			return err
		}
		paths = append(paths, members...)
	}
	slices.Sort(paths)
	for _, path := range slices.Compact(paths) {
		removed, err := catalog.removeBucket(ctx, path)
		if err != nil {
			return err
		}
		if !removed {
			return storageError(fmt.Sprintf("delete bucket %s: still in use", path), ErrResetPending)
		}
	}
	return nil
}

func familyPaths(base string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(base))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError("discover deletion family", err)
	}
	paths := []string{base}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if bucket, ok := parseBucket(base, name); ok {
			paths = append(paths, bucket.Path)
			continue
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if trimmed, found := strings.CutSuffix(name, suffix); found {
				name = trimmed
				break
			}
		}
		if bucket, ok := parseBucket(base, name); ok {
			paths = append(paths, bucket.Path)
			continue
		}
		if bucket, ok := parseBucket(base, strings.TrimSuffix(name, ".creating")); ok {
			paths = append(paths, bucket.Path+".creating")
		}
	}
	return paths, nil
}

func (catalog *Catalog) removeBucket(ctx context.Context, path string) (bool, error) {
	slog.DebugContext(ctx, "remove audit bucket", "path", path)
	if err := ctx.Err(); err != nil {
		return false, storageError("remove bucket "+path, err)
	}
	lock := catalog.bucketLock(path)
	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return false, storageError("lock deletion "+path, err)
	}
	defer func() { _ = lock.Close() }()
	if !locked {
		return false, nil
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		target := path + suffix
		info, err := os.Lstat(target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, storageError("remove bucket "+path, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := catalog.checkpoint("delete", target); err != nil {
			return false, storageError("remove bucket "+path, err)
		}
		if err := os.Remove(target); err != nil {
			return false, storageError("delete "+target, err)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		if err := syncDirectory(filepath.Dir(path)); err != nil {
			return false, storageError("remove bucket "+path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, storageError("remove bucket "+path, err)
	}
	// The catalog lock excludes new acquirers, and the exclusive bucket lock
	// proves that no handle still owns this inode.
	if err := os.Remove(lock.Path()); err != nil {
		return false, storageError("remove bucket lock", err)
	}
	return true, nil
}

func regularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storageError("stat audit bucket", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("audit bucket %s must be a regular file", path)
	}
	return nil
}

func (catalog *Catalog) checkpoint(operation string, path string) error {
	if catalog.filesystemCheckpoint == nil {
		return nil
	}
	if err := catalog.filesystemCheckpoint(operation, path); err != nil {
		return storageError(fmt.Sprintf("catalog %s %s", operation, path), err)
	}
	return nil
}
