package auditmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
)

const eventGraphPlanQuery = `
with event_graphs as (
	select event.event_id, max(julianday(receipt.received_at)) as latest_received_at,
		exists (
			select 1
			from intake_receipts related_receipt
			where related_receipt.event_id = event.event_id
				and (
					not exists (
						select 1 from gate_evaluations hot_evaluation
						where hot_evaluation.receipt_id = related_receipt.receipt_id
							and hot_evaluation.mode = 'hot'
					)
					or exists (
						select 1 from intake_deferred deferred
						where deferred.receipt_id = related_receipt.receipt_id
							and deferred.state = 'pending'
					)
					or exists (
						select 1 from deferred_audit_outbox outbox
						where outbox.receipt_id = related_receipt.receipt_id
							and outbox.state = 'pending'
					)
					or exists (
						select 1 from deferred_audit_outbox_entries entry
						where entry.receipt_id = related_receipt.receipt_id
							and entry.delivered_at is null
					)
				)
		) as protected,
		(
			exists (select 1 from intake_event_details detail
				where detail.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_layer_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_label_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from deferred_audit_outbox_entry_details detail
				join deferred_audit_outbox outbox using (receipt_id)
				where outbox.event_id = event.event_id)
		) as has_detail,
		(
			coalesce((select sum(length(detail.content))
				from intake_event_details detail where detail.event_id = event.event_id), 0)
			+ coalesce((select sum(length(detail.error_json))
				from gate_evaluation_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id), 0)
			+ coalesce((select sum(
				length(detail.input_json) + length(detail.output_json)
				+ length(detail.metadata_json) + length(detail.error_message)
			) from gate_evaluation_layer_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id), 0)
			+ coalesce((select sum(length(detail.rationale))
				from gate_evaluation_label_details detail
				join gate_evaluations evaluation using (evaluation_id)
				where evaluation.event_id = event.event_id), 0)
			+ coalesce((select sum(length(detail.payload_json))
				from deferred_audit_outbox_entry_details detail
				join deferred_audit_outbox outbox using (receipt_id)
				where outbox.event_id = event.event_id), 0)
		) as detail_bytes,
		(
			length(event.event_id) + length(event.system) + length(event.session_id)
			+ length(event.event_name) + length(event.tool_name) + length(event.command)
			+ coalesce((select sum(
				length(evaluation.evaluation_id) + length(evaluation.final_verdict)
			) from gate_evaluations evaluation
				where evaluation.event_id = event.event_id), 0)
		) as summary_bytes
	from intake_events event
	join intake_receipts receipt on receipt.event_id = event.event_id
	group by event.event_id
)
select
	coalesce(sum(case
		when protected = 0 and has_detail = 1 and latest_received_at < julianday(?) then 1
		else 0 end), 0),
	coalesce(sum(case
		when protected = 0 and latest_received_at < julianday(?) then 1
		else 0 end), 0),
	coalesce(sum(case when protected = 1 then 1 else 0 end), 0),
	coalesce(sum(case when protected = 1 then detail_bytes + summary_bytes else 0 end), 0),
	coalesce(sum(case
		when protected = 0 and latest_received_at < julianday(?)
			then detail_bytes + summary_bytes
		when protected = 0 and has_detail = 1 and latest_received_at < julianday(?)
			then detail_bytes
		else 0 end), 0)
from event_graphs
`

const snapshotCopyAttempts = 3

const (
	snapshotCopyBufferBytes = 64 * 1024
	walHeaderBytes          = 32
	walFrameHeaderBytes     = 24
)

type databaseSnapshot struct {
	database      *sql.DB
	databaseBytes int64
	walBytes      int64
	path          string
	cleanup       func()
}

type walPrefix struct {
	exists bool
	size   int64
	header [walHeaderBytes]byte
}

// Preview selects whole event graphs using one policy and clock snapshot.
func Preview(
	ctx context.Context,
	path string,
	policy config.AuditStoragePolicy,
	now time.Time,
) (Plan, error) {
	if strings.TrimSpace(path) == "" {
		return Plan{}, errors.New("audit database path is required")
	}
	if now.IsZero() {
		return Plan{}, errors.New("audit maintenance clock is required")
	}
	if policy.FullDetailRetention < 0 || policy.SummaryRetention <= 0 {
		return Plan{}, errors.New("audit retention durations are invalid")
	}
	snapshot, err := openDatabaseSnapshot(ctx, path)
	if err != nil {
		return Plan{}, err
	}
	defer snapshot.cleanup()
	return previewDatabase(ctx, snapshot.database, policy, now)
}

func previewDatabase(
	ctx context.Context,
	database *sql.DB,
	policy config.AuditStoragePolicy,
	now time.Time,
) (Plan, error) {
	if err := validateReceiptTimestamps(ctx, database); err != nil {
		return Plan{}, err
	}
	now = now.UTC()
	detailCutoff := now.Add(-policy.FullDetailRetention)
	summaryCutoff := now.Add(-policy.SummaryRetention)
	policyHash, err := hashPolicy(policy)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		PlannedAt:              now,
		PolicyHash:             policyHash,
		DetailCutoff:           &detailCutoff,
		SummaryCutoff:          summaryCutoff,
		DetailCandidateGraphs:  0,
		SummaryCandidateGraphs: 0,
		ProtectedGraphs:        0,
		ProtectedBytes:         0,
		EstimatedDeleteBytes:   0,
	}
	if err := database.QueryRowContext(
		ctx,
		eventGraphPlanQuery,
		detailCutoff.Format(time.RFC3339Nano),
		summaryCutoff.Format(time.RFC3339Nano),
		summaryCutoff.Format(time.RFC3339Nano),
		detailCutoff.Format(time.RFC3339Nano),
	).Scan(
		&plan.DetailCandidateGraphs,
		&plan.SummaryCandidateGraphs,
		&plan.ProtectedGraphs,
		&plan.ProtectedBytes,
		&plan.EstimatedDeleteBytes,
	); err != nil {
		return Plan{}, wrapError("preview audit maintenance", err)
	}
	if policy.MaxSizeBytes > 0 {
		summaryCandidates, err := simulateRetention(ctx, database, policy, now)
		if err != nil {
			return Plan{}, err
		}
		plan.SummaryCandidateGraphs = summaryCandidates
	}
	return plan, nil
}

func simulateRetention(
	ctx context.Context,
	database *sql.DB,
	policy config.AuditStoragePolicy,
	now time.Time,
) (int64, error) {
	options := ApplyOptions{
		Path: "", Policy: policy, Now: now, Owner: "", LeaseTTL: 0, Log: slog.Default(),
	}
	for {
		count, err := applyDetailBatch(ctx, database, options)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			break
		}
	}
	var summaryCandidates int64
	for {
		count, err := applySummaryBatch(ctx, database, options)
		if err != nil {
			return 0, err
		}
		summaryCandidates += count
		if count == 0 {
			break
		}
	}
	candidateQueuePrepared := false
	for {
		size, err := measureDatabaseSize(ctx, database, 0, 0)
		if err != nil {
			return 0, err
		}
		if size.CompactedUsageBytes <= policy.MaxSizeBytes {
			return summaryCandidates, nil
		}
		if !candidateQueuePrepared {
			if err := prepareSizeCandidateQueue(ctx, database); err != nil {
				return 0, err
			}
			candidateQueuePrepared = true
		}
		count, err := applyOldestSizeGraph(ctx, database)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			return summaryCandidates, nil
		}
		summaryCandidates += count
	}
}

func validateReceiptTimestamps(ctx context.Context, database *sql.DB) error {
	var receiptID int64
	var receivedAt string
	err := database.QueryRowContext(ctx, `
		select receipt_id, received_at
		from intake_receipts
		where julianday(received_at) is null
		limit 1
	`).Scan(&receiptID, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return wrapError("validate audit receipt timestamps", err)
	}
	return fmt.Errorf("invalid receipt timestamp for receipt %d: %q", receiptID, receivedAt)
}

func openDatabaseSnapshot(ctx context.Context, path string) (databaseSnapshot, error) {
	if _, err := os.Stat(path); err != nil {
		return databaseSnapshot{}, wrapError("stat audit database", err)
	}
	for range snapshotCopyAttempts {
		snapshot, stable, err := copyDatabaseSnapshot(ctx, path)
		if err != nil {
			return databaseSnapshot{}, err
		}
		if stable {
			return snapshot, nil
		}
		snapshot.cleanup()
	}
	return databaseSnapshot{}, errors.New("audit database changed while creating read-only snapshot")
}

func copyDatabaseSnapshot(
	ctx context.Context,
	sourcePath string,
) (databaseSnapshot, bool, error) {
	slog.DebugContext(ctx, "copy audit database snapshot", "path", sourcePath)
	directory, err := os.MkdirTemp("", "agent-gate-audit-snapshot-*")
	if err != nil {
		return databaseSnapshot{}, false, wrapError("create audit database snapshot directory", err)
	}
	snapshotPath := filepath.Join(directory, "audit.db")
	cleanup := func() { _ = os.RemoveAll(directory) }
	releaseLock, err := holdSnapshotCheckpointLock(ctx, sourcePath+"-shm")
	if err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = releaseLock()
		}
	}()
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		cleanup()
		return databaseSnapshot{}, false, wrapError("stat audit database snapshot source", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		cleanup()
		return databaseSnapshot{}, false, errors.New("audit database must be a regular file")
	}
	if err := streamCopyFile(ctx, sourcePath, snapshotPath, sourceInfo.Size()); err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	wal, complete, err := captureWALPrefix(ctx, sourcePath+"-wal")
	if err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	if !complete {
		return emptySnapshot(cleanup), false, nil
	}
	if wal.exists {
		if err := streamCopyFile(ctx, sourcePath+"-wal", snapshotPath+"-wal", wal.size); err != nil {
			cleanup()
			return databaseSnapshot{}, false, err
		}
	}
	stableWAL, err := validateWALPrefix(sourcePath+"-wal", wal)
	if err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	if err := releaseLock(); err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	lockHeld = false
	if !stableWAL {
		return emptySnapshot(cleanup), false, nil
	}
	database, err := openSnapshotDatabase(ctx, snapshotPath)
	if err != nil {
		cleanup()
		return databaseSnapshot{}, false, err
	}
	return databaseSnapshot{
		database:      database,
		databaseBytes: sourceInfo.Size(), walBytes: wal.size,
		path: snapshotPath,
		cleanup: func() {
			_ = database.Close()
			cleanup()
		},
	}, true, nil
}

func emptySnapshot(cleanup func()) databaseSnapshot {
	return databaseSnapshot{
		database: nil, databaseBytes: 0, walBytes: 0, path: "", cleanup: cleanup,
	}
}

func openSnapshotDatabase(ctx context.Context, path string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	uri.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, wrapError("open audit database read-only", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, wrapError("ping audit database read-only", err)
	}
	var integrity string
	if err := database.QueryRowContext(ctx, `pragma quick_check`).Scan(&integrity); err != nil {
		_ = database.Close()
		return nil, wrapError("validate audit database snapshot", err)
	}
	if integrity != "ok" {
		_ = database.Close()
		return nil, fmt.Errorf("validate audit database snapshot: %s", integrity)
	}
	return database, nil
}

func streamCopyFile(
	ctx context.Context,
	sourcePath string,
	destinationPath string,
	size int64,
) error {
	slog.DebugContext(ctx, "stream audit database snapshot file", "path", sourcePath, "bytes", size)
	source, err := os.Open(sourcePath)
	if err != nil {
		return wrapError("open audit database snapshot source", err)
	}
	defer func() { _ = source.Close() }()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return wrapError("open audit database snapshot destination", err)
	}
	defer func() { _ = destination.Close() }()
	buffer := make([]byte, snapshotCopyBufferBytes)
	written := int64(0)
	for written < size {
		if err := ctx.Err(); err != nil {
			return wrapError("copy audit database snapshot file", err)
		}
		readSize := min(int64(len(buffer)), size-written)
		readBytes, readErr := io.ReadFull(source, buffer[:readSize])
		if readErr != nil {
			return wrapError("read audit database snapshot file", readErr)
		}
		if _, err := destination.Write(buffer[:readBytes]); err != nil {
			return wrapError("write audit database snapshot file", err)
		}
		written += int64(readBytes)
	}
	if written != size {
		return fmt.Errorf(
			"audit database snapshot source changed during copy: copied %d of %d bytes",
			written,
			size,
		)
	}
	return nil
}

func captureWALPrefix(ctx context.Context, path string) (walPrefix, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return walPrefix{exists: false, size: 0, header: [walHeaderBytes]byte{}}, true, nil
	}
	if err != nil {
		return emptyWALPrefix(), false, wrapError("stat audit write-ahead log", err)
	}
	if info.Size() == 0 {
		return walPrefix{exists: false, size: 0, header: [walHeaderBytes]byte{}}, true, nil
	}
	if info.Size() < walHeaderBytes {
		return emptyWALPrefix(), false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return emptyWALPrefix(), false, wrapError("open audit write-ahead log", err)
	}
	defer func() { _ = file.Close() }()
	var header [walHeaderBytes]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return emptyWALPrefix(), false, wrapError("read audit write-ahead log header", err)
	}
	pageSize := int64(binary.BigEndian.Uint32(header[8:12]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize <= 0 {
		return emptyWALPrefix(), false, nil
	}
	frameSize := pageSize + walFrameHeaderBytes
	completeFrames := (info.Size() - walHeaderBytes) / frameSize
	committedSize := int64(walHeaderBytes)
	frameHeader := make([]byte, walFrameHeaderBytes)
	for frameIndex := range completeFrames {
		if err := ctx.Err(); err != nil {
			return emptyWALPrefix(), false, wrapError("capture audit write-ahead log", err)
		}
		offset := int64(walHeaderBytes) + frameIndex*frameSize
		if _, err := file.ReadAt(frameHeader, offset); err != nil {
			return emptyWALPrefix(), false, wrapError("read audit write-ahead log frame", err)
		}
		if !bytes.Equal(frameHeader[8:16], header[16:24]) {
			break
		}
		if binary.BigEndian.Uint32(frameHeader[4:8]) != 0 {
			committedSize = offset + frameSize
		}
	}
	return walPrefix{exists: true, size: committedSize, header: header}, true, nil
}

func emptyWALPrefix() walPrefix {
	return walPrefix{exists: false, size: 0, header: [walHeaderBytes]byte{}}
}

func validateWALPrefix(path string, captured walPrefix) (bool, error) {
	if !captured.exists {
		return true, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, wrapError("restat audit write-ahead log", err)
	}
	if info.Size() < captured.size {
		return false, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, wrapError("reopen audit write-ahead log", err)
	}
	defer func() { _ = file.Close() }()
	var currentHeader [walHeaderBytes]byte
	if _, err := io.ReadFull(file, currentHeader[:]); err != nil {
		return false, wrapError("reread audit write-ahead log header", err)
	}
	return bytes.Equal(currentHeader[16:24], captured.header[16:24]), nil
}

func hashPolicy(policy config.AuditStoragePolicy) (string, error) {
	type detailPolicy struct {
		WireInput           bool `json:"wire_input"`
		NormalizedInput     bool `json:"normalized_input"`
		ProviderEvidence    bool `json:"provider_evidence"`
		EnvironmentEvidence bool `json:"environment_evidence"`
		EvaluationContent   bool `json:"evaluation_content"`
	}
	type policySnapshot struct {
		Profile                 config.AuditStorageProfile `json:"profile"`
		MaintenanceInterval     time.Duration              `json:"maintenance_interval"`
		MaxSizeBytes            int64                      `json:"max_size_bytes"`
		MaintenanceBatchRows    int                        `json:"maintenance_batch_rows"`
		CompactAfterMaintenance bool                       `json:"compact_after_maintenance"`
		FullDetailRetention     time.Duration              `json:"full_detail_retention"`
		SummaryRetention        time.Duration              `json:"summary_retention"`
		Detail                  detailPolicy               `json:"detail"`
	}
	snapshot := policySnapshot{
		Profile:                 policy.Profile,
		MaintenanceInterval:     policy.MaintenanceInterval,
		MaxSizeBytes:            policy.MaxSizeBytes,
		MaintenanceBatchRows:    policy.MaintenanceBatchRows,
		CompactAfterMaintenance: policy.CompactAfterMaintenance,
		FullDetailRetention:     policy.FullDetailRetention,
		SummaryRetention:        policy.SummaryRetention,
		Detail: detailPolicy{
			WireInput:           policy.Detail.WireInput,
			NormalizedInput:     policy.Detail.NormalizedInput,
			ProviderEvidence:    policy.Detail.ProviderEvidence,
			EnvironmentEvidence: policy.Detail.EnvironmentEvidence,
			EvaluationContent:   policy.Detail.EvaluationContent,
		},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", wrapError("encode audit storage policy", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func wrapError(message string, err error) error {
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
