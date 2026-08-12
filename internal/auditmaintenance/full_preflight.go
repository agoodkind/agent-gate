package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	_ "github.com/mattn/go-sqlite3"

	installer "goodkind.io/agent-gate/internal/install"
)

const fullCompactMinimumSafetyBytes = uint64(64 * 1024 * 1024)

// FullCompactPlan reports whether an operator can perform full offline compaction.
type FullCompactPlan struct {
	DatabasePath      string                 `json:"database_path"`
	DatabaseSize      DatabaseSize           `json:"database_size"`
	FreeBytes         uint64                 `json:"free_bytes"`
	RequiredFreeBytes uint64                 `json:"required_free_bytes"`
	Service           installer.ServiceState `json:"service"`
	IntegrityOK       bool                   `json:"integrity_ok"`
	HookImpact        string                 `json:"hook_impact"`
	identity          fullCompactIdentity
}

type fullCompactIdentity struct {
	device string
	inode  string
}

// FullCompactOptions configures read-only full-compaction preflight.
type FullCompactOptions struct {
	Path      string
	Service   installer.ServiceState
	FreeBytes func(string) (uint64, error)
}

// PreviewFullCompact validates an operator-controlled offline compaction without writing.
func PreviewFullCompact(ctx context.Context, options FullCompactOptions) (FullCompactPlan, error) {
	if err := ctx.Err(); err != nil {
		return FullCompactPlan{}, wrapError("start full audit compaction preflight", err)
	}
	if !options.Service.Managed {
		return FullCompactPlan{}, errors.New("full compaction requires a managed service")
	}
	canonicalPath, identity, err := canonicalFullCompactDatabase(options.Path)
	if err != nil {
		return FullCompactPlan{}, err
	}
	if options.FreeBytes == nil {
		options.FreeBytes = filesystemFreeBytes
	}
	freeBytes, err := options.FreeBytes(filepath.Dir(canonicalPath))
	if err != nil {
		return FullCompactPlan{}, wrapError("measure audit database filesystem free space", err)
	}
	if err := validateFullCompactIdentity(canonicalPath, identity); err != nil {
		return FullCompactPlan{}, err
	}
	snapshot, err := openDatabaseSnapshot(ctx, canonicalPath)
	if err != nil {
		return FullCompactPlan{}, wrapError("prepare audit database integrity snapshot", err)
	}
	defer snapshot.cleanup()
	if err := checkFullCompactIntegrity(ctx, snapshot.database); err != nil {
		return FullCompactPlan{}, err
	}
	if err := validateFullCompactIdentity(canonicalPath, identity); err != nil {
		return FullCompactPlan{}, err
	}
	databaseBytes, err := fullCompactSourceBytes(snapshot.databaseBytes)
	if err != nil {
		return FullCompactPlan{}, err
	}
	walBytes, err := fullCompactSourceBytes(snapshot.walBytes)
	if err != nil {
		return FullCompactPlan{}, err
	}
	requiredBytes, err := requiredFullCompactFreeBytes(databaseBytes, walBytes)
	if err != nil {
		return FullCompactPlan{}, err
	}
	if freeBytes < requiredBytes {
		return FullCompactPlan{}, fmt.Errorf(
			"insufficient free space for full compaction: have %d bytes, require %d bytes",
			freeBytes,
			requiredBytes,
		)
	}
	return FullCompactPlan{
		DatabasePath: canonicalPath,
		DatabaseSize: DatabaseSize{
			DatabaseBytes: snapshot.databaseBytes, WALBytes: snapshot.walBytes,
			PageSizeBytes: 0, PageCount: 0, FreePages: 0, CompactedUsageBytes: 0,
		},
		FreeBytes: freeBytes, RequiredFreeBytes: requiredBytes,
		Service: options.Service, IntegrityOK: true,
		HookImpact: "operator-controlled offline interval",
		identity:   identity,
	}, nil
}

func canonicalFullCompactDatabase(path string) (string, fullCompactIdentity, error) {
	if strings.TrimSpace(path) == "" {
		return "", fullCompactIdentity{}, errors.New("audit database path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fullCompactIdentity{}, wrapError("resolve audit database absolute path", err)
	}
	inputInfo, err := os.Lstat(absolutePath)
	if err != nil {
		return "", fullCompactIdentity{}, wrapError("inspect audit database path", err)
	}
	if inputInfo.Mode()&os.ModeSymlink != 0 {
		return "", fullCompactIdentity{}, errors.New("audit database final path must not be a symbolic link")
	}
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(absolutePath))
	if err != nil {
		return "", fullCompactIdentity{}, wrapError("resolve audit database parent", err)
	}
	canonicalPath := filepath.Join(canonicalParent, filepath.Base(absolutePath))
	identity, err := inspectFullCompactIdentity(canonicalPath)
	if err != nil {
		return "", fullCompactIdentity{}, err
	}
	return canonicalPath, identity, nil
}

func inspectFullCompactIdentity(path string) (fullCompactIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fullCompactIdentity{}, wrapError("inspect canonical audit database", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fullCompactIdentity{}, errors.New("audit database final path must not be a symbolic link")
	}
	if !info.Mode().IsRegular() {
		return fullCompactIdentity{}, errors.New("audit database must be a regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fullCompactIdentity{}, errors.New("audit database identity is unavailable")
	}
	return fullCompactIdentity{
		device: formatFullCompactDevice(stat), inode: strconv.FormatUint(stat.Ino, 10),
	}, nil
}

func validateFullCompactIdentity(path string, expected fullCompactIdentity) error {
	actual, err := inspectFullCompactIdentity(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("audit database path changed during full compaction preflight")
	}
	return nil
}

func requiredFullCompactFreeBytes(databaseBytes uint64, walBytes uint64) (uint64, error) {
	if databaseBytes > math.MaxUint64-walBytes {
		return 0, errors.New("audit database size exceeds supported range")
	}
	sourceBytes := databaseBytes + walBytes
	safetyBytes := max(fullCompactMinimumSafetyBytes, sourceBytes/10)
	if sourceBytes > math.MaxUint64-safetyBytes {
		return 0, errors.New("full compaction free-space requirement exceeds supported range")
	}
	return sourceBytes + safetyBytes, nil
}

func fullCompactSourceBytes(value int64) (uint64, error) {
	if value < 0 {
		return 0, errors.New("audit database size is invalid")
	}
	return uint64(value), nil // #nosec G115 -- negative sizes are rejected above.
}

func filesystemFreeBytes(path string) (uint64, error) {
	var state syscall.Statfs_t
	if err := syscall.Statfs(path, &state); err != nil {
		return 0, wrapError("measure filesystem free space", err)
	}
	if state.Bsize <= 0 {
		return 0, errors.New("filesystem block size is invalid")
	}
	blockSize := uint64(state.Bsize) // #nosec G115 -- the positive value is validated above.
	availableBlocks := state.Bavail
	if blockSize != 0 && availableBlocks > math.MaxUint64/blockSize {
		return 0, errors.New("filesystem free-space measurement exceeds supported range")
	}
	return availableBlocks * blockSize, nil
}

func checkFullCompactIntegrity(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `pragma integrity_check`)
	if err != nil {
		return wrapError("run audit database integrity check", err)
	}
	defer func() { _ = rows.Close() }()
	problems := make([]string, 0)
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return wrapError("read audit database integrity result", err)
		}
		if result != "ok" {
			problems = append(problems, result)
		}
	}
	if err := rows.Err(); err != nil {
		return wrapError("read audit database integrity results", err)
	}
	if len(problems) != 0 {
		return fmt.Errorf("audit database integrity check failed: %s", strings.Join(problems, "; "))
	}
	return nil
}
