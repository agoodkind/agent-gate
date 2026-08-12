package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// ErrNotFound reports that an evaluation identity does not exist.
var ErrNotFound = errors.New("evaluation not found")

// Store reads and writes evaluations through the intake database connection.
type Store struct {
	database *sql.DB
}

// NewStore initializes evaluation storage over a database owned by its caller.
func NewStore(ctx context.Context, database *sql.DB) (*Store, error) {
	store := &Store{database: database}
	if err := store.initialize(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// RecordCompleted atomically stores one completed evaluation and its children.
func (s *Store) RecordCompleted(ctx context.Context, record Record) error {
	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("begin evaluation transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	if err := s.RecordCompletedInTx(ctx, transaction, record); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrapError("commit evaluation transaction", err)
	}
	return nil
}

// RecordCompletedInTx stores one complete evaluation inside a caller-owned
// transaction. The caller remains responsible for commit or rollback.
func (s *Store) RecordCompletedInTx(
	ctx context.Context,
	transaction *sql.Tx,
	record Record,
) error {
	if transaction == nil {
		return errors.New("evaluation transaction is required")
	}
	if err := validateRecord(record); err != nil {
		return err
	}
	if err := insertEvaluation(
		ctx,
		transaction,
		record.Evaluation,
		len(record.Layers),
		len(record.Labels),
	); err != nil {
		return err
	}
	for _, layer := range record.Layers {
		if err := insertLayer(ctx, transaction, record.Evaluation.EvaluationID, layer); err != nil {
			return err
		}
	}
	for _, label := range record.Labels {
		if err := insertLabel(ctx, transaction, record.Evaluation.EvaluationID, label); err != nil {
			return err
		}
	}
	return nil
}

// Get returns one complete evaluation with ordered layers and labels.
func (s *Store) Get(ctx context.Context, evaluationID string) (Record, error) {
	evaluation, err := s.getEvaluation(ctx, evaluationID)
	if err != nil {
		return Record{}, err
	}
	layers, err := s.getLayers(ctx, evaluationID)
	if err != nil {
		return Record{}, err
	}
	labels, err := s.getLabels(ctx, evaluationID)
	if err != nil {
		return Record{}, err
	}
	return Record{Evaluation: evaluation, Layers: layers, Labels: labels}, nil
}

func (s *Store) initialize(ctx context.Context) error {
	if err := auditstorage.Migrate(ctx, s.database); err != nil {
		return wrapError("migrate evaluation schema", err)
	}
	return nil
}

func validateRecord(record Record) error {
	if record.Evaluation.EvaluationID == "" {
		return errors.New("evaluation id is required")
	}
	if len(record.Evaluation.ErrorJSON) > 0 && !json.Valid(record.Evaluation.ErrorJSON) {
		return errors.New("evaluation error JSON is invalid")
	}
	for index, layer := range record.Layers {
		if err := validateStoredLayer(layer, index); err != nil {
			return err
		}
	}
	for _, label := range record.Labels {
		if label.Confidence == nil {
			continue
		}
		if math.IsNaN(*label.Confidence) || math.IsInf(*label.Confidence, 0) {
			return fmt.Errorf("label %q confidence must be finite", label.Namespace)
		}
		if *label.Confidence < 0 || *label.Confidence > 1 {
			return fmt.Errorf("label %q confidence must be between 0 and 1", label.Namespace)
		}
	}
	return nil
}

func validateStoredLayer(layer Layer, index int) error {
	if layer.LayerIndex != index {
		return fmt.Errorf("layer index %d is not ordered position %d", layer.LayerIndex, index)
	}
	if !json.Valid(layer.InputJSON) {
		return fmt.Errorf("layer index %d input JSON is invalid", layer.LayerIndex)
	}
	if !json.Valid(layer.OutputJSON) {
		return fmt.Errorf("layer index %d output JSON is invalid", layer.LayerIndex)
	}
	if !json.Valid(layer.MetadataJSON) {
		return fmt.Errorf("layer index %d metadata JSON is invalid", layer.LayerIndex)
	}
	if _, err := UnmarshalLayerMetadata(layer.MetadataJSON); err != nil {
		return fmt.Errorf("layer index %d metadata is invalid: %s", layer.LayerIndex, err.Error())
	}
	if err := validateLayerSemantics(layer.Kind, layer.Status, layer.Outcome); err != nil {
		return fmt.Errorf("layer index %d semantics are invalid: %s", layer.LayerIndex, err.Error())
	}
	if err := validateLayerOutputHash(layer.OutputJSON, layer.OutputHash); err != nil {
		return fmt.Errorf("layer index %d output is invalid: %s", layer.LayerIndex, err.Error())
	}
	if layer.ParentLayerIndex == nil {
		return nil
	}
	if *layer.ParentLayerIndex < 0 || *layer.ParentLayerIndex >= index {
		return fmt.Errorf(
			"layer index %d has invalid parent index %d",
			layer.LayerIndex,
			*layer.ParentLayerIndex,
		)
	}
	return nil
}

func insertEvaluation(
	ctx context.Context,
	transaction *sql.Tx,
	value Evaluation,
	layerCount int,
	labelCount int,
) error {
	_, err := transaction.ExecContext(ctx, `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, error_json,
			layer_count, label_count
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		value.EvaluationID,
		value.ReceiptID,
		value.EventID,
		value.Attempt,
		value.Mode,
		value.ConfigHash,
		value.EngineVersion,
		value.EngineCommit,
		value.EngineBuildHash,
		value.InputHash,
		formatTime(value.StartedAt),
		formatTime(value.CompletedAt),
		value.FinalVerdict,
		value.FinalSource,
		value.EnforcementAction,
		value.Enforced,
		value.TotalLatencyUS,
		[]byte(value.ErrorJSON),
		layerCount,
		labelCount,
	)
	if err != nil {
		return wrapError("insert evaluation", err)
	}
	return nil
}

func insertLayer(
	ctx context.Context,
	transaction *sql.Tx,
	evaluationID string,
	value Layer,
) error {
	_, err := transaction.ExecContext(ctx, `
		insert into gate_evaluation_layers (
			evaluation_id, layer_index, parent_layer_index, kind, name, status, outcome, verdict,
			input_reference, input_json, input_hash, output_hash, output_json,
			metadata_json, started_at, completed_at, latency_us, service_name, service_version,
			model_name, model_version, prompt_hash, schema_hash, cache_status,
			cache_key_hash, cache_entry_version, cache_expires_at, error_code,
			error_message, retry_count
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		evaluationID,
		value.LayerIndex,
		value.ParentLayerIndex,
		value.Kind,
		value.Name,
		value.Status,
		value.Outcome,
		value.Verdict,
		value.InputReference,
		[]byte(value.InputJSON),
		value.InputHash,
		value.OutputHash,
		[]byte(value.OutputJSON),
		[]byte(value.MetadataJSON),
		formatTime(value.StartedAt),
		formatTime(value.CompletedAt),
		value.LatencyUS,
		value.ServiceName,
		value.ServiceVersion,
		value.ModelName,
		value.ModelVersion,
		value.PromptHash,
		value.SchemaHash,
		value.CacheStatus,
		value.CacheKeyHash,
		value.CacheEntryVersion,
		formatOptionalTime(value.CacheExpiresAt),
		value.ErrorCode,
		value.ErrorMessage,
		value.RetryCount,
	)
	if err != nil {
		return wrapError(fmt.Sprintf("insert evaluation layer %d", value.LayerIndex), err)
	}
	return nil
}

func insertLabel(
	ctx context.Context,
	transaction *sql.Tx,
	evaluationID string,
	value Label,
) error {
	_, err := transaction.ExecContext(ctx, `
		insert into gate_evaluation_labels (
			evaluation_id, namespace, label_version, verdict, source,
			confidence, rationale, created_at
		) values (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		evaluationID,
		value.Namespace,
		value.LabelVersion,
		value.Verdict,
		value.Source,
		value.Confidence,
		value.Rationale,
		formatTime(value.CreatedAt),
	)
	if err != nil {
		message := fmt.Sprintf(
			"insert evaluation label %q version %d",
			value.Namespace,
			value.LabelVersion,
		)
		return wrapError(message, err)
	}
	return nil
}

func (s *Store) getEvaluation(ctx context.Context, evaluationID string) (Evaluation, error) {
	var value Evaluation
	var startedAt string
	var completedAt string
	err := s.database.QueryRowContext(ctx, `
		select evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, error_json
		from gate_evaluations
		where evaluation_id = ?
	`, evaluationID).Scan(
		&value.EvaluationID,
		&value.ReceiptID,
		&value.EventID,
		&value.Attempt,
		&value.Mode,
		&value.ConfigHash,
		&value.EngineVersion,
		&value.EngineCommit,
		&value.EngineBuildHash,
		&value.InputHash,
		&startedAt,
		&completedAt,
		&value.FinalVerdict,
		&value.FinalSource,
		&value.EnforcementAction,
		&value.Enforced,
		&value.TotalLatencyUS,
		&value.ErrorJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Evaluation{}, ErrNotFound
	}
	if err != nil {
		return Evaluation{}, wrapError("read evaluation", err)
	}
	value.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return Evaluation{}, err
	}
	value.CompletedAt, err = parseTime(completedAt)
	if err != nil {
		return Evaluation{}, err
	}
	return value, nil
}

func (s *Store) getLayers(ctx context.Context, evaluationID string) ([]Layer, error) {
	rows, err := s.database.QueryContext(ctx, `
		select layer_index, parent_layer_index, kind, name, status, outcome, verdict,
			input_reference, input_json, input_hash, output_hash, output_json,
			metadata_json, started_at, completed_at, latency_us, service_name, service_version,
			model_name, model_version, prompt_hash, schema_hash, cache_status,
			cache_key_hash, cache_entry_version, cache_expires_at, error_code,
			error_message, retry_count
		from gate_evaluation_layers
		where evaluation_id = ?
		order by layer_index
	`, evaluationID)
	if err != nil {
		return nil, wrapError("query evaluation layers", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	layers := make([]Layer, 0)
	for rows.Next() {
		layer, err := scanLayer(rows)
		if err != nil {
			return nil, err
		}
		layers = append(layers, layer)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate evaluation layers", err)
	}
	return layers, nil
}

func scanLayer(rows *sql.Rows) (Layer, error) {
	var value Layer
	var parentIndex sql.NullInt64
	var cacheVersion sql.NullInt64
	var cacheExpiry sql.NullString
	var metadataJSON []byte
	var startedAt string
	var completedAt string
	err := rows.Scan(
		&value.LayerIndex,
		&parentIndex,
		&value.Kind,
		&value.Name,
		&value.Status,
		&value.Outcome,
		&value.Verdict,
		&value.InputReference,
		&value.InputJSON,
		&value.InputHash,
		&value.OutputHash,
		&value.OutputJSON,
		&metadataJSON,
		&startedAt,
		&completedAt,
		&value.LatencyUS,
		&value.ServiceName,
		&value.ServiceVersion,
		&value.ModelName,
		&value.ModelVersion,
		&value.PromptHash,
		&value.SchemaHash,
		&value.CacheStatus,
		&value.CacheKeyHash,
		&cacheVersion,
		&cacheExpiry,
		&value.ErrorCode,
		&value.ErrorMessage,
		&value.RetryCount,
	)
	if err != nil {
		return Layer{}, wrapError("scan evaluation layer", err)
	}
	if parentIndex.Valid {
		converted := int(parentIndex.Int64)
		value.ParentLayerIndex = &converted
	}
	value.MetadataJSON = json.RawMessage(metadataJSON)
	if cacheVersion.Valid {
		value.CacheEntryVersion = &cacheVersion.Int64
	}
	value.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return Layer{}, err
	}
	value.CompletedAt, err = parseTime(completedAt)
	if err != nil {
		return Layer{}, err
	}
	if cacheExpiry.Valid {
		parsed, err := parseTime(cacheExpiry.String)
		if err != nil {
			return Layer{}, err
		}
		value.CacheExpiresAt = &parsed
	}
	return value, nil
}

func (s *Store) getLabels(ctx context.Context, evaluationID string) ([]Label, error) {
	rows, err := s.database.QueryContext(ctx, `
		select namespace, label_version, verdict, source, confidence,
			rationale, created_at
		from gate_evaluation_labels
		where evaluation_id = ?
		order by namespace, label_version
	`, evaluationID)
	if err != nil {
		return nil, wrapError("query evaluation labels", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	labels := make([]Label, 0)
	for rows.Next() {
		var label Label
		var confidence sql.NullFloat64
		var createdAt string
		if err := rows.Scan(
			&label.Namespace,
			&label.LabelVersion,
			&label.Verdict,
			&label.Source,
			&confidence,
			&label.Rationale,
			&createdAt,
		); err != nil {
			return nil, wrapError("scan evaluation label", err)
		}
		if confidence.Valid {
			label.Confidence = &confidence.Float64
		}
		label.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate evaluation labels", err)
	}
	return labels, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(value *time.Time) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(*value), Valid: true}
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, wrapError("parse evaluation time", err)
	}
	return parsed, nil
}

func wrapError(message string, err error) error {
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
