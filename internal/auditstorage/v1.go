package auditstorage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func migrateV1(ctx context.Context, transaction *sql.Tx) error {
	if err := executeStatements(ctx, transaction, intakeBaseSchema); err != nil {
		return err
	}
	if err := ensureIntakeColumns(ctx, transaction); err != nil {
		return err
	}
	if err := ensureDeferredSchema(ctx, transaction); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, intakeIndexes); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, evaluationSchema); err != nil {
		return err
	}
	if err := ensureEvaluationColumns(ctx, transaction); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, evaluationIndexes); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, outboxSchema); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, auditSchema); err != nil {
		return err
	}
	return ensureCommandFTS(ctx, transaction)
}

var intakeBaseSchema = []string{
	`create table if not exists intake_events (
		seq integer primary key autoincrement,
		event_id text not null unique,
		schema_version integer not null,
		recorded_at text not null,
		system text not null,
		session_id text not null,
		turn_id text not null,
		event_name text not null,
		tool_name text not null,
		tool_use_id text not null,
		cwd text not null,
		effective_cwd text not null,
		command text not null,
		file_path text not null,
		raw_payload blob not null,
		raw_payload_hash text not null,
		normalized_json text not null,
		classification_json text not null default '{}',
		env_fingerprint_json text not null default '{}',
		hot_eval_latency_us integer
	)`,
	`create table if not exists intake_receipts (
		receipt_id integer primary key autoincrement,
		event_id text not null,
		received_at text not null,
		foreign key(event_id) references intake_events(event_id) on delete cascade,
		unique(receipt_id, event_id)
	)`,
	`create unique index if not exists intake_receipts_identity_idx
		on intake_receipts(receipt_id, event_id)`,
	`create table if not exists intake_deferred_repairs (
		event_id text primary key,
		state text not null,
		pending_at text,
		completed_at text,
		last_replay_at text,
		replay_count integer not null default 0,
		repair_error text not null
	)`,
}

var intakeIndexes = []string{
	`create index if not exists intake_events_recorded_at_idx on intake_events(recorded_at)`,
	`create index if not exists intake_events_session_recorded_at_idx on intake_events(session_id, recorded_at)`,
	`create index if not exists intake_events_system_recorded_at_idx on intake_events(system, recorded_at)`,
	`create index if not exists intake_deferred_state_idx on intake_deferred(state)`,
	`create index if not exists intake_deferred_event_id_idx on intake_deferred(event_id)`,
	`create index if not exists intake_deferred_claim_expiry_idx on intake_deferred(state, claim_expires_at)`,
	`create index if not exists intake_receipts_event_id_idx on intake_receipts(event_id)`,
	`create index if not exists intake_receipts_received_at_idx on intake_receipts(received_at)`,
	`create index if not exists intake_events_event_name_idx on intake_events(event_name)`,
	`create index if not exists intake_events_tool_name_idx on intake_events(tool_name)`,
	`create index if not exists intake_events_tool_use_id_idx on intake_events(tool_use_id)`,
	`create index if not exists intake_events_turn_id_idx on intake_events(turn_id)`,
	`create index if not exists intake_events_cwd_idx on intake_events(cwd)`,
	`create index if not exists intake_events_effective_cwd_idx on intake_events(effective_cwd)`,
	`create index if not exists intake_events_file_path_idx on intake_events(file_path)`,
	`create index if not exists intake_events_raw_payload_hash_idx on intake_events(raw_payload_hash)`,
	`create index if not exists intake_events_schema_version_idx on intake_events(schema_version)`,
	`create index if not exists intake_events_effective_cwd_recorded_at_idx on intake_events(effective_cwd, recorded_at)`,
	`create index if not exists intake_events_tool_name_recorded_at_idx on intake_events(tool_name, recorded_at)`,
	`create index if not exists intake_events_event_name_recorded_at_idx on intake_events(event_name, recorded_at)`,
	`create index if not exists intake_events_hot_eval_latency_us_idx on intake_events(hot_eval_latency_us)`,
}

var evaluationSchema = []string{
	`create table if not exists gate_evaluations (
		evaluation_id text primary key,
		receipt_id integer not null,
		event_id text not null,
		attempt integer not null,
		mode text not null,
		config_hash text not null,
		engine_version text not null,
		engine_commit text not null,
		engine_build_hash text not null,
		input_hash text not null,
		started_at text not null,
		completed_at text not null,
		final_verdict text not null,
		final_source text not null,
		enforcement_action text not null,
		enforced integer not null,
		total_latency_us integer not null,
		error_json blob,
		layer_count integer not null default -1,
		label_count integer not null default -1,
		foreign key(receipt_id, event_id)
			references intake_receipts(receipt_id, event_id),
		foreign key(event_id) references intake_events(event_id)
	)`,
	`create table if not exists gate_evaluation_layers (
		evaluation_id text not null,
		layer_index integer not null,
		parent_layer_index integer,
		kind text not null,
		name text not null,
		status text not null,
		outcome text not null default '',
		verdict text not null default '',
		input_reference text not null,
		input_json blob,
		input_hash text not null,
		output_hash text not null,
		output_json blob,
		metadata_json blob not null default '{}',
		started_at text not null,
		completed_at text not null,
		latency_us integer not null,
		service_name text not null,
		service_version text not null,
		model_name text not null,
		model_version text not null,
		prompt_hash text not null,
		schema_hash text not null,
		cache_status text not null,
		cache_key_hash text not null,
		cache_entry_version integer,
		cache_expires_at text,
		error_code text not null,
		error_message text not null,
		retry_count integer not null,
		primary key(evaluation_id, layer_index),
		foreign key(evaluation_id) references gate_evaluations(evaluation_id)
			on delete cascade,
		foreign key(evaluation_id, parent_layer_index)
			references gate_evaluation_layers(evaluation_id, layer_index)
	)`,
	`create table if not exists gate_evaluation_labels (
		evaluation_id text not null,
		namespace text not null,
		label_version integer not null,
		verdict text not null,
		source text not null,
		confidence real,
		rationale text not null,
		created_at text not null,
		primary key(evaluation_id, namespace, label_version),
		foreign key(evaluation_id) references gate_evaluations(evaluation_id)
			on delete cascade
	)`,
}

var evaluationIndexes = []string{
	`create index if not exists gate_evaluations_event_id_idx on gate_evaluations(event_id)`,
	`create index if not exists gate_evaluations_receipt_id_idx on gate_evaluations(receipt_id)`,
	`create unique index if not exists gate_evaluations_receipt_mode_attempt_idx on gate_evaluations(receipt_id, mode, attempt)`,
	`create index if not exists gate_evaluations_completed_at_idx on gate_evaluations(completed_at)`,
	`create index if not exists gate_evaluations_final_verdict_idx on gate_evaluations(final_verdict)`,
	`create index if not exists gate_evaluation_layers_kind_name_idx on gate_evaluation_layers(kind, name)`,
	`create index if not exists gate_evaluation_layers_model_idx on gate_evaluation_layers(model_name, model_version)`,
	`create index if not exists gate_evaluation_layers_cache_status_idx on gate_evaluation_layers(cache_status)`,
	`create index if not exists gate_evaluation_layers_outcome_idx on gate_evaluation_layers(outcome)`,
	`create index if not exists gate_evaluation_layers_verdict_idx on gate_evaluation_layers(verdict)`,
	`create index if not exists gate_evaluation_labels_verdict_idx on gate_evaluation_labels(verdict)`,
	`create index if not exists gate_evaluation_labels_source_idx on gate_evaluation_labels(source)`,
}

var outboxSchema = []string{
	`create table if not exists deferred_audit_outbox (
		receipt_id integer primary key,
		event_id text not null,
		evaluation_id text not null unique,
		state text not null,
		created_at text not null,
		completed_at text,
		claim_owner text,
		claim_expires_at text,
		claim_attempt integer not null default 0,
		foreign key(receipt_id, event_id)
			references intake_receipts(receipt_id, event_id) on delete cascade,
		foreign key(evaluation_id) references gate_evaluations(evaluation_id)
			on delete cascade,
		check(state in ('pending', 'complete'))
	)`,
	`create table if not exists deferred_audit_outbox_entries (
		receipt_id integer not null,
		entry_index integer not null,
		audit_event_id text not null,
		payload_json blob not null,
		delivered_at text,
		primary key(receipt_id, entry_index),
		foreign key(receipt_id) references deferred_audit_outbox(receipt_id)
			on delete cascade
	)`,
	`create index if not exists deferred_audit_outbox_pending_idx on deferred_audit_outbox(state, claim_expires_at)`,
}

var auditSchema = []string{
	`create table if not exists events (
		event_id text primary key,
		schema_version integer,
		time text,
		level text,
		message text,
		system text,
		session_id text,
		turn_id text,
		event_name text,
		tool_use_id text,
		tool_name text,
		raw_payload_hash text
	)`,
	`create table if not exists operations (
		event_id text primary key,
		cwd text,
		effective_cwd text,
		command text,
		file_path text
	)`,
	`create table if not exists decisions (
		event_id text primary key,
		kind text,
		can_block integer,
		rules_checked_json text,
		rules_matched_json text
	)`,
	`create table if not exists violations (
		id integer primary key autoincrement,
		event_id text,
		rule text,
		mode text,
		field_path text,
		file_path text,
		start integer,
		end integer,
		message text
	)`,
	`create index if not exists events_time_idx on events(time)`,
	`create index if not exists events_system_time_idx on events(system, time)`,
	`create index if not exists events_session_time_idx on events(session_id, time)`,
	`create index if not exists events_tool_time_idx on events(tool_name, time)`,
	`create index if not exists events_event_name_time_idx on events(event_name, time)`,
	`create index if not exists decisions_kind_idx on decisions(kind)`,
	`create index if not exists violations_rule_idx on violations(rule)`,
	`create index if not exists violations_mode_idx on violations(mode)`,
}

func executeStatements(ctx context.Context, transaction *sql.Tx, statements []string) error {
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return wrapError("execute audit schema statement", err)
		}
	}
	return nil
}

func ensureIntakeColumns(ctx context.Context, transaction *sql.Tx) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "env_fingerprint_json", definition: "text not null default '{}'"},
		{name: "classification_json", definition: "text not null default '{}'"},
		{name: "hot_eval_latency_us", definition: "integer"},
	}
	for _, column := range columns {
		if err := ensureColumn(
			ctx,
			transaction,
			"intake_events",
			column.name,
			column.definition,
		); err != nil {
			return err
		}
	}
	return nil
}

func ensureDeferredSchema(ctx context.Context, transaction *sql.Tx) error {
	exists, err := tableExists(ctx, transaction, "intake_deferred")
	if err != nil {
		return err
	}
	if !exists {
		return executeStatements(ctx, transaction, []string{currentDeferredSchema})
	}
	hasReceiptID, err := columnExists(ctx, transaction, "intake_deferred", "receipt_id")
	if err != nil {
		return err
	}
	if !hasReceiptID {
		return migrateLegacyDeferredRows(ctx, transaction)
	}
	columns := []struct {
		name       string
		definition string
		seed       string
	}{
		{name: "claim_owner", definition: "text", seed: ""},
		{name: "claim_expires_at", definition: "text", seed: ""},
		{
			name:       "claim_attempt",
			definition: "integer not null default 0",
			seed:       `update intake_deferred set claim_attempt = replay_count`,
		},
	}
	for _, column := range columns {
		found, err := columnExists(ctx, transaction, "intake_deferred", column.name)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		if err := addColumn(ctx, transaction, "intake_deferred", column.name, column.definition); err != nil {
			return err
		}
		if column.seed != "" {
			if _, err := transaction.ExecContext(ctx, column.seed); err != nil {
				return wrapError("seed intake_deferred."+column.name, err)
			}
		}
	}
	return nil
}

const currentDeferredSchema = `create table intake_deferred (
	receipt_id integer primary key,
	event_id text not null,
	state text not null,
	pending_at text,
	completed_at text,
	last_replay_at text,
	replay_count integer not null default 0,
	claim_owner text,
	claim_expires_at text,
	claim_attempt integer not null default 0,
	foreign key(receipt_id, event_id)
		references intake_receipts(receipt_id, event_id) on delete cascade,
	check(state in ('none', 'pending', 'complete'))
)`

func migrateLegacyDeferredRows(ctx context.Context, transaction *sql.Tx) error {
	statements := []string{
		`alter table intake_deferred rename to intake_deferred_legacy`,
		currentDeferredSchema,
		`insert into intake_deferred (
			receipt_id, event_id, state, pending_at, completed_at,
			last_replay_at, replay_count, claim_attempt
		)
		select receipt.receipt_id, legacy.event_id, legacy.state, legacy.pending_at,
			legacy.completed_at, legacy.last_replay_at, legacy.replay_count,
			legacy.replay_count
		from intake_deferred_legacy legacy
		join intake_receipts receipt on receipt.receipt_id = (
			select max(candidate.receipt_id)
			from intake_receipts candidate
			where candidate.event_id = legacy.event_id
		)`,
		`insert or replace into intake_deferred_repairs (
			event_id, state, pending_at, completed_at, last_replay_at,
			replay_count, repair_error
		)
		select legacy.event_id, legacy.state, legacy.pending_at,
			legacy.completed_at, legacy.last_replay_at, legacy.replay_count,
			'missing_receipt'
		from intake_deferred_legacy legacy
		where not exists (
			select 1 from intake_receipts receipt
			where receipt.event_id = legacy.event_id
		)`,
		`drop table intake_deferred_legacy`,
	}
	return executeStatements(ctx, transaction, statements)
}

func ensureEvaluationColumns(ctx context.Context, transaction *sql.Tx) error {
	columns := []struct {
		table      string
		name       string
		definition string
	}{
		{table: "gate_evaluation_layers", name: "metadata_json", definition: "blob not null default '{}'"},
		{table: "gate_evaluation_layers", name: "outcome", definition: "text not null default ''"},
		{table: "gate_evaluation_layers", name: "verdict", definition: "text not null default ''"},
		{table: "gate_evaluations", name: "layer_count", definition: "integer not null default -1"},
		{table: "gate_evaluations", name: "label_count", definition: "integer not null default -1"},
	}
	for _, column := range columns {
		if err := ensureColumn(
			ctx,
			transaction,
			column.table,
			column.name,
			column.definition,
		); err != nil {
			return err
		}
	}
	return nil
}

func ensureColumn(
	ctx context.Context,
	transaction *sql.Tx,
	table string,
	column string,
	definition string,
) error {
	found, err := columnExists(ctx, transaction, table, column)
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	return addColumn(ctx, transaction, table, column, definition)
}

func addColumn(
	ctx context.Context,
	transaction *sql.Tx,
	table string,
	column string,
	definition string,
) error {
	statement := fmt.Sprintf("alter table %s add column %s %s", table, column, definition)
	if _, err := transaction.ExecContext(ctx, statement); err != nil {
		return wrapError(fmt.Sprintf("add %s.%s", table, column), err)
	}
	return nil
}

func tableExists(ctx context.Context, transaction *sql.Tx, table string) (bool, error) {
	var count int
	if err := transaction.QueryRowContext(
		ctx,
		`select count(*) from sqlite_schema where type = 'table' and name = ?`,
		table,
	).Scan(&count); err != nil {
		return false, wrapError("inspect table "+table, err)
	}
	return count != 0, nil
}

func columnExists(
	ctx context.Context,
	transaction *sql.Tx,
	table string,
	column string,
) (bool, error) {
	rows, err := transaction.QueryContext(ctx, "pragma table_info("+table+")")
	if err != nil {
		return false, wrapError("inspect "+table+" columns", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, wrapError("scan "+table+" columns", err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapError("iterate "+table+" columns", err)
	}
	return false, nil
}

func ensureCommandFTS(ctx context.Context, transaction *sql.Tx) error {
	alreadyExisted, err := tableExists(ctx, transaction, "intake_command_fts")
	if err != nil {
		return err
	}
	_, err = transaction.ExecContext(ctx, `create virtual table if not exists intake_command_fts using fts5(
		command,
		content='intake_events',
		content_rowid='seq',
		tokenize='trigram'
	)`)
	if err != nil && strings.Contains(err.Error(), "no such module: fts5") {
		return nil
	}
	if err != nil {
		return wrapError("create intake command search index", err)
	}
	triggers := []string{
		`create trigger if not exists intake_events_ai after insert on intake_events begin
			insert into intake_command_fts(rowid, command) values (new.seq, new.command);
		end`,
		`create trigger if not exists intake_events_ad after delete on intake_events begin
			insert into intake_command_fts(intake_command_fts, rowid, command) values('delete', old.seq, old.command);
		end`,
		`create trigger if not exists intake_events_au after update of command on intake_events begin
			insert into intake_command_fts(intake_command_fts, rowid, command) values('delete', old.seq, old.command);
			insert into intake_command_fts(rowid, command) values (new.seq, new.command);
		end`,
	}
	if err := executeStatements(ctx, transaction, triggers); err != nil {
		return err
	}
	if alreadyExisted {
		return nil
	}
	if _, err := transaction.ExecContext(
		ctx,
		`insert into intake_command_fts(intake_command_fts) values('rebuild')`,
	); err != nil {
		return wrapError("backfill intake command search index", err)
	}
	return nil
}
