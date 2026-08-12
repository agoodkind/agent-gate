pragma foreign_keys = on;

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
    raw_payload blob not null,
    raw_payload_hash text not null,
    normalized_json text not null,
    classification_json text not null default '{}',
    env_fingerprint_json text not null default '{}',
    hot_eval_latency_us integer
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
    foreign key(receipt_id, event_id)
        references intake_receipts(receipt_id, event_id) on delete cascade,
    check(state in ('none', 'pending', 'complete'))
);
create table intake_deferred_repairs (
    event_id text primary key,
    state text not null,
    pending_at text,
    completed_at text,
    last_replay_at text,
    replay_count integer not null default 0,
    repair_error text not null
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
    error_json blob,
    layer_count integer not null default -1,
    label_count integer not null default -1,
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
);
create table gate_evaluation_labels (
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
);
create table deferred_audit_outbox (
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
);
create table deferred_audit_outbox_entries (
    receipt_id integer not null,
    entry_index integer not null,
    audit_event_id text not null,
    payload_json blob not null,
    delivered_at text,
    primary key(receipt_id, entry_index),
    foreign key(receipt_id) references deferred_audit_outbox(receipt_id)
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
    file_path text
);
create table decisions (
    event_id text primary key,
    kind text,
    can_block integer,
    rules_checked_json text,
    rules_matched_json text
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
    message text
);

insert into intake_events values (
    1, 'event-legacy', 1, '2026-05-09T19:26:30Z', 'codex', 'session-legacy',
    'turn-legacy', 'PreToolUse', 'Shell', 'tool-legacy', '/repo', '/repo',
    'echo legacy', '', cast('{"wire":"legacy"}' as blob), 'sha256:legacy',
    '{"normalized":"legacy"}',
    '{"resolved_provider":"codex","result":"resolved"}',
    '{"CODEX_THREAD_ID":"legacy-thread"}', 123
);
insert into intake_receipts values (1, 'event-legacy', '2026-05-09T19:26:31Z');
insert into intake_deferred values (
    1, 'event-legacy', 'pending', '2026-05-09T19:26:32Z', null, null, 0,
    null, null, 0
);
insert into gate_evaluations values (
    'eval-legacy', 1, 'event-legacy', 1, 'hot', 'config-hash', '1.0.0',
    'commit', 'build-hash', 'input-hash', '2026-05-09T19:26:33Z',
    '2026-05-09T19:26:34Z', 'block', 'rule', 'deny', 1, 1000,
    cast('{"evaluation_error":"legacy"}' as blob), 1, 1
);
insert into gate_evaluation_layers values (
    'eval-legacy', 0, null, 'inference', 'legacy-layer', 'complete', 'match',
    'block', 'normalized', cast('{"input":"legacy"}' as blob),
    'input-hash-legacy', 'output-hash-legacy', cast('{"output":"legacy"}' as blob),
    cast('{"schema_version":2,"rule_name":"legacy-rule-filter","condition_index":3,"verified_provenance":{"requested_model":"model-legacy","reported_prompt_hash_status":"absent","reported_schema_hash_status":"absent"},"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"present","raw":{"request_id":"request-legacy","requested_model":"model-legacy","prompt_tokens":"101","cached_tokens":"17","completion_tokens":"19"}}}' as blob),
    '2026-05-09T19:26:33Z', '2026-05-09T19:26:34Z', 500,
    'service-legacy', 'service-version-legacy', 'model-summary-legacy',
    'model-version-legacy', 'prompt-hash-legacy', 'schema-hash-legacy',
    'miss', 'cache-key-legacy', 23, '2026-05-10T19:26:34Z',
    'error-code-legacy', 'legacy layer error', 29
);
insert into gate_evaluation_labels values (
    'eval-legacy', 'operator', 1, 'block', 'review', 1.0,
    'legacy label rationale',
    '2026-05-09T19:26:35Z'
);
insert into deferred_audit_outbox values (
    1, 'event-legacy', 'eval-legacy', 'pending', '2026-05-09T19:26:36Z',
    null, null, null, 0
);
insert into deferred_audit_outbox_entries values (
    1, 0, 'audit-legacy', x'7b7d', null
);
insert into events values (
    'audit-legacy', 1, '2026-05-09T19:26:36Z', 'info', 'hook.blocked',
    'codex', 'session-legacy', 'turn-legacy', 'PreToolUse', 'tool-legacy',
    'Shell', 'sha256:legacy'
);
insert into operations values ('audit-legacy', '/repo', '/repo', 'echo legacy', '');
insert into decisions values ('audit-legacy', 'block', 1, '["legacy-rule"]', '["legacy-rule"]');
insert into violations values (
    1, 'audit-legacy', 'legacy-rule', 'block', 'command', '', 0, 11, 'blocked'
);
