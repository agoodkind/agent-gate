package auditstorage

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const sortRunHeaderBytes = 8

func (catalog *Catalog) sortFilePrefix() string {
	digest := sha256.Sum256([]byte(catalog.options.StatePath))
	return fmt.Sprintf(".audit-deferred-sort-%x-", digest[:8])
}

func (catalog *Catalog) cleanSortFiles(ctx context.Context) error {
	prefix := catalog.sortFilePrefix()
	return visitDirectory(ctx, catalog.options.StatePath, func(entry os.DirEntry) error {
		if !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return storageError("inspect interrupted metadata sort", err)
		}
		// Sort files receive data only after unlinking. A named nonempty file
		// cannot come from this sorter.
		if info.Size() != 0 {
			return nil
		}
		slog.DebugContext(ctx, "remove interrupted deferred metadata sort", "path", entry.Name())
		if err := os.Remove(filepath.Join(filepath.Dir(catalog.options.StatePath), entry.Name())); err != nil {
			return storageError("remove interrupted metadata sort", err)
		}
		return nil
	})
}

func (catalog *Catalog) anonymousSortFile(ctx context.Context) (*os.File, error) {
	slog.DebugContext(ctx, "open deferred metadata sort", "state", catalog.options.StatePath)
	file, err := os.CreateTemp(filepath.Dir(catalog.options.StatePath), catalog.sortFilePrefix()+"*.tmp")
	if err != nil {
		return nil, storageError("create deferred metadata sort", err)
	}
	// macOS and Linux retain an open descriptor after unlinking. Process exit
	// releases the storage even when cancellation interrupts a merge.
	if err := os.Remove(file.Name()); err != nil {
		return nil, storageError("unlink deferred metadata sort", errors.Join(err, file.Close()))
	}
	return file, nil
}

type sortRun struct{ reader *bufio.Reader }

func openSortRun(file *os.File, offset int64) (sortRun, int64, error) {
	var header [sortRunHeaderBytes]byte
	if _, err := file.ReadAt(header[:], offset); err != nil {
		return sortRun{}, offset, storageError("read metadata sort header", err)
	}
	size := binary.LittleEndian.Uint64(header[:])
	if size > math.MaxInt64 {
		return sortRun{}, offset, errors.New("metadata sort length overflows int64")
	}
	length := int64(size)
	if offset < 0 || offset > math.MaxInt64-sortRunHeaderBytes || length > math.MaxInt64-offset-sortRunHeaderBytes {
		return sortRun{}, offset, errors.New("metadata sort length overflows offset")
	}
	start := offset + sortRunHeaderBytes
	return sortRun{reader: bufio.NewReader(io.NewSectionReader(file, start, length))}, start + length, nil
}

func (run sortRun) next() (string, bool, error) {
	line, err := run.reader.ReadBytes('\n')
	if errors.Is(err, io.EOF) && len(line) == 0 {
		return "", false, nil
	}
	if err != nil {
		return "", false, storageError("read sorted metadata path", err)
	}
	var path string
	if err := json.Unmarshal(line, &path); err != nil {
		return "", false, storageError("decode sorted metadata path", err)
	}
	return path, true, nil
}

func writeSortRun(ctx context.Context, file *os.File, visit func(func(string) error) error) (uint64, error) {
	start, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, storageError("locate metadata sort run", err)
	}
	var header [sortRunHeaderBytes]byte
	if _, err := file.Write(header[:]); err != nil {
		return 0, storageError("write metadata sort header", err)
	}
	var size uint64
	err = visit(func(path string) error {
		if err := ctx.Err(); err != nil {
			return storageError("write metadata sort", err)
		}
		data, err := json.Marshal(path)
		if err != nil {
			return storageError("encode metadata sort path", err)
		}
		data = append(data, '\n')
		_, err = file.Write(data)
		size += uint64(len(data))
		if err != nil {
			return storageError("write metadata sort path", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	binary.LittleEndian.PutUint64(header[:], size)
	if _, err := file.WriteAt(header[:], start); err != nil {
		return 0, storageError("complete metadata sort header", err)
	}
	return size + 2*sortRunHeaderBytes, nil
}

func (catalog *Catalog) mergeSortRuns(left, right sortRun, write func(string) error) error {
	leftPath, leftOK, err := left.next()
	if err != nil {
		return err
	}
	rightPath, rightOK, err := right.next()
	if err != nil {
		return err
	}
	for leftOK || rightOK {
		if leftOK && (!rightOK || catalog.compareStorageFiles(storageFile{path: leftPath, bytes: 0}, storageFile{path: rightPath, bytes: 0}) <= 0) {
			if err := write(leftPath); err != nil {
				return err
			}
			leftPath, leftOK, err = left.next()
		} else {
			if err := write(rightPath); err != nil {
				return err
			}
			rightPath, rightOK, err = right.next()
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (catalog *Catalog) mergeSortPass(ctx context.Context, input, output *os.File, runs int) (uint64, error) {
	if err := output.Truncate(0); err != nil {
		return 0, storageError("truncate metadata merge output", err)
	}
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		return 0, storageError("reset metadata merge output", err)
	}
	var offset int64
	var written uint64
	for index := 0; index < runs; index += 2 {
		left, next, err := openSortRun(input, offset)
		if err != nil {
			return written, err
		}
		offset = next
		right := sortRun{reader: bufio.NewReader(strings.NewReader(""))}
		if index+1 < runs {
			right, offset, err = openSortRun(input, offset)
			if err != nil {
				return written, err
			}
		}
		bytes, err := writeSortRun(ctx, output, func(write func(string) error) error { return catalog.mergeSortRuns(left, right, write) })
		if err != nil {
			return written, err
		}
		written += bytes
	}
	return written, nil
}

type cleanupSorter struct {
	catalog *Catalog
	input   *os.File
	output  *os.File
	files   []storageFile
	runs    int
	paths   int
	written uint64
}

func (catalog *Catalog) sortBlockedCleanup(ctx context.Context, policy RotationPolicy, now time.Time, selected []string, visit func(string) error) (resultErr error) {
	sorter := &cleanupSorter{catalog: catalog, input: nil, output: nil, files: make([]storageFile, 0, maintenanceBatchLimit), runs: 0, paths: 0, written: 0}
	defer func() { resultErr = errors.Join(resultErr, sorter.close()) }()
	if err := catalog.visitBlockedCleanup(ctx, policy, now, selected, func(path string) error { return sorter.add(ctx, path) }); err != nil {
		return err
	}
	return sorter.finish(ctx, visit)
}

func (sorter *cleanupSorter) close() error {
	var result error
	if sorter.input != nil {
		result = errors.Join(result, sorter.input.Close())
	}
	if sorter.output != nil {
		result = errors.Join(result, sorter.output.Close())
	}
	if result != nil {
		return storageError("close deferred metadata sort", result)
	}
	return nil
}

func (sorter *cleanupSorter) add(ctx context.Context, path string) error {
	if len(sorter.files) == maintenanceBatchLimit {
		if err := sorter.spill(ctx); err != nil {
			return err
		}
	}
	sorter.files = append(sorter.files, storageFile{path: path, bytes: 0})
	sorter.paths++
	return nil
}

func (sorter *cleanupSorter) spill(ctx context.Context) error {
	if sorter.input == nil {
		if err := sorter.catalog.cleanSortFiles(ctx); err != nil {
			return err
		}
		file, err := sorter.catalog.anonymousSortFile(ctx)
		if err != nil {
			return err
		}
		sorter.input = file
	}
	return sorter.flush(ctx)
}

func (sorter *cleanupSorter) flush(ctx context.Context) error {
	if len(sorter.files) == 0 {
		return nil
	}
	slices.SortFunc(sorter.files, sorter.catalog.compareStorageFiles)
	bytes, err := writeSortRun(ctx, sorter.input, func(write func(string) error) error {
		for _, file := range sorter.files {
			if err := write(file.path); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sorter.written += bytes
	sorter.runs++
	sorter.files = sorter.files[:0]
	return nil
}

func (sorter *cleanupSorter) finish(ctx context.Context, visit func(string) error) error {
	if sorter.input == nil {
		slices.SortFunc(sorter.files, sorter.catalog.compareStorageFiles)
		slog.DebugContext(ctx, "sorted deferred metadata", "paths", sorter.paths, "passes", 0, "temporary_bytes", 0)
		for _, file := range sorter.files {
			if err := ctx.Err(); err != nil {
				return storageError("emit deferred metadata", err)
			}
			if err := visit(file.path); err != nil {
				return err
			}
		}
		return nil
	}
	if err := sorter.flush(ctx); err != nil {
		return err
	}
	output, err := sorter.catalog.anonymousSortFile(ctx)
	if err != nil {
		return err
	}
	sorter.output = output
	passes := 1
	for sorter.runs > 1 {
		bytes, err := sorter.catalog.mergeSortPass(ctx, sorter.input, sorter.output, sorter.runs)
		if err != nil {
			return err
		}
		sorter.written += bytes
		sorter.input, sorter.output = sorter.output, sorter.input
		sorter.runs = (sorter.runs + 1) / 2
		passes++
	}
	slog.DebugContext(ctx, "sorted deferred metadata", "paths", sorter.paths, "passes", passes, "temporary_bytes", sorter.written)
	run, _, err := openSortRun(sorter.input, 0)
	if err != nil {
		return err
	}
	return emitSortedCleanup(ctx, run, visit)
}

func emitSortedCleanup(ctx context.Context, run sortRun, visit func(string) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return storageError("emit deferred metadata", err)
		}
		path, exists, err := run.next()
		if err != nil || !exists {
			return err
		}
		if err := visit(path); err != nil {
			return err
		}
	}
}
