package auditstorage

import (
	"context"
	"database/sql"
)

var evaluationV3DetailSchema = []string{
	`create table if not exists gate_evaluation_details (
			evaluation_id text primary key,
			error_json blob,
			foreign key(evaluation_id) references gate_evaluations(evaluation_id)
				on delete cascade
		)`,
	`create table if not exists gate_evaluation_layer_details (
			evaluation_id text not null,
			layer_index integer not null,
			input_json blob,
			output_json blob,
			metadata_json blob not null,
			error_message text not null,
			primary key(evaluation_id, layer_index),
			foreign key(evaluation_id, layer_index)
				references gate_evaluation_layers(evaluation_id, layer_index)
				on delete cascade
		)`,
	`create table if not exists gate_evaluation_label_details (
			evaluation_id text not null,
			namespace text not null,
			label_version integer not null,
			rationale text not null,
			primary key(evaluation_id, namespace, label_version),
			foreign key(evaluation_id, namespace, label_version)
				references gate_evaluation_labels(evaluation_id, namespace, label_version)
				on delete cascade
		)`,
}

var evaluationV3Statements = []string{
	`insert into gate_evaluation_details (evaluation_id, error_json)
			select evaluation_id, error_json from gate_evaluations`,
	`insert into gate_evaluation_layer_details (
			evaluation_id, layer_index, input_json, output_json, metadata_json, error_message
		)
		select evaluation_id, layer_index, input_json, output_json, metadata_json, error_message
		from gate_evaluation_layers`,
	`insert into gate_evaluation_label_details (
			evaluation_id, namespace, label_version, rationale
		)
		select evaluation_id, namespace, label_version, rationale
		from gate_evaluation_labels`,
	`create table gate_evaluations_v3 (
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
			layer_count integer not null default -1,
			label_count integer not null default -1,
			detail_state text not null,
			foreign key(receipt_id, event_id)
				references intake_receipts(receipt_id, event_id),
			foreign key(event_id) references intake_events(event_id),
			check(detail_state in ('available', 'expired', 'not_recorded', 'protected'))
		)`,
	`insert into gate_evaluations_v3 (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count, label_count,
			detail_state
		)
		select evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count, label_count,
			'available'
		from gate_evaluations`,
	`create table gate_evaluation_layers_v3 (
			evaluation_id text not null,
			layer_index integer not null,
			parent_layer_index integer,
			kind text not null,
			name text not null,
			status text not null,
			outcome text not null default '',
			verdict text not null default '',
			input_reference text not null,
			input_hash text not null,
			output_hash text not null,
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
			retry_count integer not null,
			rule_name text not null default '',
			checked_rules_json text not null default '[]',
			upstream_metadata_status text not null default '',
			request_id text not null default '',
			requested_model text not null default '',
			prompt_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			completion_tokens integer not null default 0,
			primary key(evaluation_id, layer_index),
			foreign key(evaluation_id) references gate_evaluations_v3(evaluation_id)
				on delete cascade,
			foreign key(evaluation_id, parent_layer_index)
				references gate_evaluation_layers_v3(evaluation_id, layer_index)
		)`,
	`insert into gate_evaluation_layers_v3 (
			evaluation_id, layer_index, parent_layer_index, kind, name, status, outcome,
			verdict, input_reference, input_hash, output_hash, started_at, completed_at,
			latency_us, service_name, service_version, model_name, model_version,
			prompt_hash, schema_hash, cache_status, cache_key_hash, cache_entry_version,
			cache_expires_at, error_code, retry_count, rule_name, checked_rules_json,
			upstream_metadata_status, request_id, requested_model, prompt_tokens,
			cached_tokens, completion_tokens
		)
		select evaluation_id, layer_index, parent_layer_index, kind, name, status, outcome,
			verdict, input_reference, input_hash, output_hash, started_at, completed_at,
			latency_us, service_name, service_version, model_name, model_version,
			prompt_hash, schema_hash, cache_status, cache_key_hash, cache_entry_version,
			cache_expires_at, error_code, retry_count,
			coalesce(json_extract(metadata_json, '$.rule_name'), ''),
			coalesce(json_extract(metadata_json, '$.checked_rules'), '[]'),
			coalesce(json_extract(metadata_json, '$.upstream_metadata.status'), ''),
			coalesce(json_extract(metadata_json, '$.upstream_metadata.raw.request_id'), ''),
			coalesce(
				json_extract(metadata_json, '$.upstream_metadata.raw.requested_model'),
				json_extract(metadata_json, '$.verified_provenance.requested_model'),
				''
			),
			coalesce(cast(json_extract(
				metadata_json, '$.upstream_metadata.raw.prompt_tokens'
			) as integer), 0),
			coalesce(cast(json_extract(
				metadata_json, '$.upstream_metadata.raw.cached_tokens'
			) as integer), 0),
			coalesce(cast(json_extract(
				metadata_json, '$.upstream_metadata.raw.completion_tokens'
			) as integer), 0)
		from gate_evaluation_layers`,
	`create table gate_evaluation_labels_v3 (
			evaluation_id text not null,
			namespace text not null,
			label_version integer not null,
			verdict text not null,
			source text not null,
			confidence real,
			created_at text not null,
			primary key(evaluation_id, namespace, label_version),
			foreign key(evaluation_id) references gate_evaluations_v3(evaluation_id)
				on delete cascade
		)`,
	`insert into gate_evaluation_labels_v3 (
			evaluation_id, namespace, label_version, verdict, source, confidence, created_at
		)
		select evaluation_id, namespace, label_version, verdict, source, confidence, created_at
		from gate_evaluation_labels`,
	`drop table gate_evaluation_labels`,
	`drop table gate_evaluation_layers`,
	`drop table gate_evaluations`,
	`alter table gate_evaluations_v3 rename to gate_evaluations`,
	`alter table gate_evaluation_layers_v3 rename to gate_evaluation_layers`,
	`alter table gate_evaluation_labels_v3 rename to gate_evaluation_labels`,
}

func migrateEvaluationV3(ctx context.Context, transaction *sql.Tx) error {
	if err := executeStatements(ctx, transaction, evaluationV3DetailSchema); err != nil {
		return err
	}
	hasCompatibilityDetail, err := columnExists(
		ctx,
		transaction,
		"gate_evaluations",
		"error_json",
	)
	if err != nil {
		return err
	}
	if !hasCompatibilityDetail {
		return executeStatements(ctx, transaction, evaluationIndexes)
	}
	if err := executeStatements(ctx, transaction, evaluationV3Statements); err != nil {
		return err
	}
	return executeStatements(ctx, transaction, evaluationIndexes)
}
