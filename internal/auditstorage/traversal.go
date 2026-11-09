package auditstorage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const maintenanceBatchLimit = 100

// The extra oldest entries include the current file and the first file that
// a 100-target deletion pass must skip under size pressure.
const maintenanceLookahead = maintenanceBatchLimit + 2

type pruningInventory struct {
	oldest  []storageFile
	expired []storageFile
	blocked *storageFile
	total   int64
}

func visitDirectory(ctx context.Context, base string, visit func(os.DirEntry) error) error {
	directory, err := os.Open(filepath.Dir(base))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return storageError("open audit directory", err)
	}

	defer func() { _ = directory.Close() }()

	for {
		if err := ctx.Err(); err != nil {
			return storageError("visit audit directory", err)
		}

		entries, err := directory.ReadDir(maintenanceBatchLimit)
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return storageError("read audit directory", err)
		}

		for _, entry := range entries {
			if entry.Type().IsRegular() {
				if err := visit(entry); err != nil {
					return err
				}
			}
		}
	}
}

func visitBuckets(ctx context.Context, base string, visit func(Bucket) error) error {
	return visitDirectory(
		ctx,
		base,
		func(entry os.DirEntry) error {
			bucket, valid := parseBucket(base, entry.Name())
			if !valid {
				return nil
			}

			return visit(bucket)
		},
	)
}

func familyPrimary(base, name string) (string, bool) {
	if name == filepath.Base(base) {
		return base, true
	}

	if bucket, valid := parseBucket(base, name); valid {
		return bucket.Path, true
	}

	if plain, staging := strings.CutSuffix(name, ".creating"); staging {
		if bucket, valid := parseBucket(base, plain); valid {
			return bucket.Path + ".creating", true
		}
	}

	return "", false
}

func visitFamily(ctx context.Context, base string, visit func(string) error) error {
	return visitDirectory(
		ctx,
		base,
		func(entry os.DirEntry) error {
			if primary, valid := familyPrimary(base, entry.Name()); valid {
				return visit(primary)
			}

			suffixes := []string{"-wal", "-shm", "-journal"}
			for index, suffix := range suffixes {
				plain, matches := strings.CutSuffix(entry.Name(), suffix)
				if !matches {
					continue
				}

				primary, valid := familyPrimary(base, plain)
				if !valid {
					return nil
				}

				for _, earlier := range append([]string{""}, suffixes[:index]...) {
					if _, err := os.Lstat(primary + earlier); err == nil {
						return nil
					} else if !errors.Is(err, os.ErrNotExist) {
						return storageError("stat audit family entry", err)
					}
				}

				return visit(primary)
			}

			return nil
		},
	)
}

func (catalog *Catalog) pruningInventory(
	ctx context.Context,
	policy RotationPolicy,
	now time.Time,
) (pruningInventory, error) {
	var inventory pruningInventory
	err := visitFamily(
		ctx,
		catalog.options.BasePath,
		func(path string) error {
			size, err := bucketBytes(path)
			if err != nil {
				return err
			}

			if size == 0 {
				return nil
			}

			inventory.total += size
			if inventory.total < 0 {
				return errors.New("audit storage size overflows bytes")
			}

			file := storageFile{path: path, bytes: size}
			inventory.oldest = catalog.insertMaintenanceFile(
				inventory.oldest,
				file,
				maintenanceLookahead,
			)
			bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(path))
			if !recognized || !retained(bucket, policy, now) {
				available, err := catalog.pruneAvailable(path)
				if err != nil {
					return err
				}

				if available {
					inventory.expired = catalog.insertMaintenanceFile(
						inventory.expired,
						file,
						maintenanceBatchLimit,
					)
				} else if inventory.blocked == nil || catalog.compareStorageFiles(
					file,
					*inventory.blocked,
				) < 0 {
					inventory.blocked = &file
				}
			}

			return nil
		},
	)

	return inventory, err
}

func (catalog *Catalog) insertMaintenanceFile(
	files []storageFile,
	file storageFile,
	limit int,
) []storageFile {
	position, _ := slices.BinarySearchFunc(files, file, catalog.compareStorageFiles)
	if position >= limit {
		return files
	}

	files = slices.Insert(files, position, file)

	return files[:min(len(files), limit)]
}

func (catalog *Catalog) mergeMaintenanceFiles(oldest, selected []storageFile) []storageFile {
	files := make([]storageFile, 0, len(oldest)+len(selected))
	files = append(files, oldest...)
	files = append(files, selected...)
	slices.SortFunc(files, catalog.compareStorageFiles)

	return slices.CompactFunc(
		files,
		func(left, right storageFile) bool { return left.path == right.path },
	)
}

func (catalog *Catalog) compareStorageFiles(left, right storageFile) int {
	leftBucket, _ := parseBucket(catalog.options.BasePath, filepath.Base(left.path))
	rightBucket, _ := parseBucket(catalog.options.BasePath, filepath.Base(right.path))
	if compared := strings.Compare(leftBucket.ID, rightBucket.ID); compared != 0 {
		return compared
	}

	return strings.Compare(left.path, right.path)
}
