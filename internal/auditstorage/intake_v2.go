package auditstorage

import (
	"context"
	"database/sql"
)

func migrateIntakeV2(ctx context.Context, transaction *sql.Tx) error {
	schema := []string{
		`create table if not exists intake_event_details (
			event_id text not null,
			detail_class text not null,
			content blob not null,
			primary key(event_id, detail_class),
			foreign key(event_id) references intake_events(event_id) on delete cascade
		)`,
		`create table if not exists intake_event_detail_manifest (
			event_id text primary key,
			recorded_classes_json text not null,
			available_classes_json text not null,
			state text not null,
			state_changed_at text not null,
			foreign key(event_id) references intake_events(event_id) on delete cascade,
			check(state in ('available', 'expired', 'not_recorded', 'protected'))
		)`,
	}
	if err := executeStatements(ctx, transaction, schema); err != nil {
		return err
	}
	hasCompatibilityDetail, err := columnExists(
		ctx,
		transaction,
		"intake_events",
		"raw_payload",
	)
	if err != nil {
		return err
	}
	if !hasCompatibilityDetail {
		if err := executeStatements(ctx, transaction, intakeIndexes); err != nil {
			return err
		}
		return ensureCommandFTS(ctx, transaction)
	}

	if _, err := transaction.ExecContext(ctx, `
		insert into intake_event_detail_manifest (
			event_id, recorded_classes_json, available_classes_json, state, state_changed_at
		)
		select event_id, ?, ?, 'available', recorded_at from intake_events
	`, compactDetailClassesJSON, compactDetailClassesJSON); err != nil {
		return wrapError("migrate intake detail manifest", err)
	}
	statements := []string{
		`insert into intake_event_details (event_id, detail_class, content)
			select event_id, 'wire_input', raw_payload from intake_events`,
		`insert into intake_event_details (event_id, detail_class, content)
			select event_id, 'normalized_input', cast(normalized_json as blob) from intake_events`,
		`insert into intake_event_details (event_id, detail_class, content)
			select event_id, 'provider_evidence', cast(classification_json as blob) from intake_events`,
		`insert into intake_event_details (event_id, detail_class, content)
			select event_id, 'environment_evidence', cast(env_fingerprint_json as blob) from intake_events`,
		`drop trigger if exists intake_events_ai`,
		`drop trigger if exists intake_events_ad`,
		`drop trigger if exists intake_events_au`,
		`drop table if exists intake_command_fts`,
		`create table intake_events_v2 (
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
			raw_payload_hash text not null,
			hot_eval_latency_us integer
		)`,
		`insert into intake_events_v2 (
			seq, event_id, schema_version, recorded_at, system, session_id, turn_id,
			event_name, tool_name, tool_use_id, cwd, effective_cwd, command, file_path,
			raw_payload_hash, hot_eval_latency_us
		)
		select seq, event_id, schema_version, recorded_at, system, session_id, turn_id,
			event_name, tool_name, tool_use_id, cwd, effective_cwd, command, file_path,
			raw_payload_hash, hot_eval_latency_us
		from intake_events`,
		`drop table intake_events`,
		`alter table intake_events_v2 rename to intake_events`,
	}
	if err := executeStatements(ctx, transaction, statements); err != nil {
		return err
	}
	if err := executeStatements(ctx, transaction, intakeIndexes); err != nil {
		return err
	}
	return ensureCommandFTS(ctx, transaction)
}

const compactDetailClassesJSON = `["wire_input","normalized_input","provider_evidence","environment_evidence"]`
