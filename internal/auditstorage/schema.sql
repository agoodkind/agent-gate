create table intake_events (
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
    hot_eval_latency_us integer,
    wire_input blob,
    normalized_input blob,
    provider_evidence blob,
    environment_evidence blob,
    recorded_detail_mask integer not null
);
create table intake_receipts (
    receipt_id integer primary key autoincrement,
    event_id text not null,
    received_at text not null,
    foreign key(event_id) references intake_events(event_id) on delete cascade,
    unique(receipt_id, event_id)
);
create table intake_deferred (
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
    next_attempt_at text,
    foreign key(receipt_id, event_id)
    references intake_receipts(receipt_id, event_id) on delete cascade,
    check(state in ('none', 'pending', 'complete'))
);
create table gate_evaluations (
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
    content_recorded integer not null,
    error_json blob,
    foreign key(receipt_id, event_id)
    references intake_receipts(receipt_id, event_id),
    foreign key(event_id) references intake_events(event_id)
);
create table gate_evaluation_layers (
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
    input_json blob,
    output_json blob,
    metadata_json blob,
    error_message text,
    primary key(evaluation_id, layer_index),
    foreign key(evaluation_id) references gate_evaluations(evaluation_id)
    on delete cascade,
    foreign key(evaluation_id, parent_layer_index)
    references gate_evaluation_layers(evaluation_id, layer_index)
);
create table gate_evaluation_labels (
    evaluation_id text not null,
    namespace text not null,
    label_version integer not null,
    verdict text not null,
    source text not null,
    confidence real,
    created_at text not null,
    rationale text,
    primary key(evaluation_id, namespace, label_version),
    foreign key(evaluation_id) references gate_evaluations(evaluation_id)
    on delete cascade
);
create table events (
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
);
create table operations (
    event_id text primary key,
    cwd text,
    effective_cwd text,
    command text,
    file_path text,
    foreign key(event_id) references events(event_id) on delete cascade
);
create table decisions (
    event_id text primary key,
    kind text,
    can_block integer,
    rules_checked_json text,
    rules_matched_json text,
    foreign key(event_id) references events(event_id) on delete cascade
);
create table violations (
    id integer primary key autoincrement,
    event_id text,
    rule text,
    mode text,
    field_path text,
    file_path text,
    start integer,
    end integer,
    message text,
    foreign key(event_id) references events(event_id) on delete cascade
);
create index intake_time_idx on intake_events(recorded_at, seq);
create index intake_system_time_idx on intake_events(system, recorded_at, seq);
create index intake_session_time_idx on intake_events(session_id, recorded_at, seq);
create index intake_event_time_idx on intake_events(event_name, recorded_at, seq);
create index intake_tool_time_idx on intake_events(tool_name, recorded_at, seq);
create index receipt_event_idx on intake_receipts(event_id, receipt_id);
create index receipt_time_idx on intake_receipts(received_at, receipt_id);
create index deferred_due_idx on intake_deferred(state, next_attempt_at, receipt_id);
create index deferred_event_idx on intake_deferred(event_id, state);
create index evaluation_event_idx on gate_evaluations(event_id);
create unique index evaluation_attempt_idx on gate_evaluations(receipt_id, mode, attempt);
create index evaluation_time_idx on gate_evaluations(completed_at, evaluation_id);
create index evaluation_verdict_time_idx on gate_evaluations(final_verdict, completed_at, evaluation_id);
create index audit_time_idx on events(time, event_id);
create index audit_system_time_idx on events(system, time, event_id);
create index audit_session_time_idx on events(session_id, time, event_id);
create index audit_event_time_idx on events(event_name, time, event_id);
create index audit_tool_time_idx on events(tool_name, time, event_id);
create index decision_kind_idx on decisions(kind, event_id);
create index violation_rule_idx on violations(rule, event_id);
create index violation_event_idx on violations(event_id);
