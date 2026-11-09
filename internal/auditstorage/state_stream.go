package auditstorage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
)

type catalogField string

const (
	fieldCleanupError catalogField = "cleanup_error"
	fieldBasePath     catalogField = "base_path"
	fieldInterval     catalogField = "interval_seconds"
	fieldRetained     catalogField = "retention_buckets"
	fieldCurrent      catalogField = "current_bucket_id"
	fieldSequence     catalogField = "segment_sequence"
	fieldReset        catalogField = "reset_pending"
	fieldDeferred     catalogField = "deferred_files"
	fieldPending      catalogField = "pending_base_paths"
)

func (catalog *Catalog) walkState(
	ctx context.Context,
	visit func(catalogField, *json.Decoder) error,
) (bool, error) {
	file, err := os.Open(catalog.options.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	if err != nil {
		return false, storageError("open catalog state", err)
	}

	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	token, err := decoder.Token()
	if err != nil {
		return true, storageError("decode catalog state", err)
	}

	if token != json.Delim('{') {
		return true, errors.New("catalog state must be a JSON object")
	}

	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return true, storageError("read catalog state", err)
		}

		token, err := decoder.Token()
		if err != nil {
			return true, storageError("decode catalog field", err)
		}

		key, valid := token.(string)
		if !valid {
			return true, errors.New("catalog field must be a string")
		}

		if err := visit(catalogField(key), decoder); err != nil {
			return true, err
		}
	}

	if _, err := decoder.Token(); err != nil {
		return true, storageError("finish catalog state", err)
	}

	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return true, errors.New("catalog state contains trailing data")
	}

	return true, nil
}

func (catalog *Catalog) loadStateSummary(ctx context.Context) (CatalogState, error) {
	state, _, err := catalog.readStateSummary(ctx)
	return state, err
}

func (catalog *Catalog) readStateSummary(ctx context.Context) (CatalogState, int, error) {
	var state CatalogState
	deferredCount := 0
	exists, err := catalog.walkState(
		ctx,
		func(key catalogField, decoder *json.Decoder) error {
			var err error
			switch key {
			case fieldCleanupError:
				err = decoder.Decode(&state.CleanupError)
			case fieldBasePath:
				err = decoder.Decode(&state.BasePath)
			case fieldInterval:
				err = decoder.Decode(&state.IntervalSeconds)
			case fieldRetained:
				err = decoder.Decode(&state.Retained)
			case fieldCurrent:
				err = decoder.Decode(&state.CurrentBucketID)
			case fieldSequence:
				err = decoder.Decode(&state.SegmentSequence)
			case fieldReset:
				err = decoder.Decode(&state.ResetPending)
			case fieldDeferred:
				state.DeferredFiles = nil
				deferredCount = 0

				return readStateStrings(
					ctx,
					decoder,
					func(path string) error {
						deferredCount++
						if len(state.DeferredFiles) < maintenanceBatchLimit {
							state.DeferredFiles = append(state.DeferredFiles, path)
						}

						return nil
					},
				)
			case fieldPending:
				return skipStateValue(ctx, decoder)
			default:
				return skipStateValue(ctx, decoder)
			}

			if err != nil {
				return storageError("decode catalog field", err)
			}

			return nil
		},
	)
	if err == nil && exists && state.BasePath == "" {
		err = errors.New("catalog state has no base path")
	}

	return state, deferredCount, err
}

func readStateStrings(ctx context.Context, decoder *json.Decoder, visit func(string) error) error {
	token, err := decoder.Token()
	if err != nil {
		return storageError("decode catalog paths", err)
	}

	if token == nil {
		return nil
	}

	if token != json.Delim('[') {
		return errors.New("catalog paths must be a JSON array")
	}

	for decoder.More() {
		if err := ctx.Err(); err != nil {
			return storageError("read catalog paths", err)
		}

		var path string
		if err := decoder.Decode(&path); err != nil {
			return storageError("decode catalog path", err)
		}

		if err := visit(path); err != nil {
			return err
		}
	}

	if _, err := decoder.Token(); err != nil {
		return storageError("finish catalog paths", err)
	}

	return nil
}

func skipStateValue(ctx context.Context, decoder *json.Decoder) error {
	if err := ctx.Err(); err != nil {
		return storageError("read catalog value", err)
	}

	token, err := decoder.Token()
	if err != nil {
		return storageError("decode catalog value", err)
	}

	delimiter, nested := token.(json.Delim)
	if !nested || delimiter != '[' && delimiter != '{' {
		return nil
	}

	for decoder.More() {
		if err := skipStateValue(ctx, decoder); err != nil {
			return err
		}
	}

	if _, err := decoder.Token(); err != nil {
		return storageError("finish catalog value", err)
	}

	return nil
}

func (catalog *Catalog) visitStatePaths(
	ctx context.Context,
	key catalogField,
	visit func(string) error,
) error {
	_, err := catalog.walkState(
		ctx,
		func(field catalogField, decoder *json.Decoder) error {
			if field == key {
				return readStateStrings(ctx, decoder, visit)
			}

			return skipStateValue(ctx, decoder)
		},
	)

	return err
}

func (catalog *Catalog) saveStatePreserving(
	ctx context.Context,
	state CatalogState,
	field catalogField,
) error {
	var additions []string
	switch field {
	case fieldDeferred:
		state.DeferredFiles = nil
	case fieldPending:
		additions = state.PendingBasePaths
		state.PendingBasePaths = nil
	case fieldCleanupError,
		fieldBasePath,
		fieldInterval,
		fieldRetained,
		fieldCurrent,
		fieldSequence,
		fieldReset:
		fallthrough
	default:
		return fmt.Errorf("unsupported preserved catalog field %s", field)
	}

	return catalog.publishState(func(file *os.File) error {
		data, err := json.Marshal(state)
		if err != nil {
			return storageError("encode catalog state", err)
		}

		if _, err := file.Write(data[:len(data)-1]); err != nil {
			return storageError("write catalog state", err)
		}

		return catalog.writePreservedArray(ctx, file, field, additions)
	})
}

func (catalog *Catalog) writePreservedArray(
	ctx context.Context,
	file *os.File,
	field catalogField,
	additions []string,
) error {
	return writeStatePathArray(
		file,
		field,
		func(writePath func(string) error) error {
			if err := catalog.visitStatePaths(
				ctx,
				field,
				func(path string) error {
					if slices.Contains(additions, path) {
						return nil
					}

					return writePath(path)
				},
			); err != nil {
				return err
			}

			for _, path := range additions {
				if err := writePath(path); err != nil {
					return err
				}
			}

			return nil
		},
	)
}

func writeStatePathArray(
	file *os.File,
	field catalogField,
	visit func(func(string) error) error,
) error {
	if _, err := fmt.Fprintf(file, ",%q:[", field); err != nil {
		return storageError("write catalog paths", err)
	}

	first := true
	writePath := func(path string) error {
		if !first {
			if _, err := file.WriteString(","); err != nil {
				return storageError("write catalog separator", err)
			}
		}

		first = false
		data, err := json.Marshal(path)
		if err != nil {
			return storageError("encode catalog path", err)
		}

		if _, err := file.Write(data); err != nil {
			return storageError("write catalog path", err)
		}

		return nil
	}
	if err := visit(writePath); err != nil {
		return err
	}

	if _, err := file.WriteString("]}"); err != nil {
		return storageError("finish catalog state", err)
	}

	return nil
}
