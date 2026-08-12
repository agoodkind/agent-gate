pragma foreign_keys = off;

with recursive orphan(number) as (
    select 1
    union all
    select number + 1 from orphan where number < 20
)
insert into gate_evaluations (
    evaluation_id,
    receipt_id,
    event_id,
    attempt,
    mode,
    config_hash,
    engine_version,
    engine_commit,
    engine_build_hash,
    input_hash,
    started_at,
    completed_at,
    final_verdict,
    final_source,
    enforcement_action,
    enforced,
    total_latency_us,
    error_json,
    layer_count,
    label_count
)
select
    printf('eval-orphan-%02d', number),
    1000 + number,
    printf('event-orphan-%02d', number),
    1,
    'hot',
    'orphan-config-hash',
    '1.0.0',
    'orphan-commit',
    'orphan-build-hash',
    printf('orphan-input-hash-%02d', number),
    printf('2026-05-09T20:%02d:00Z', number),
    printf('2026-05-09T20:%02d:01Z', number),
    'allow',
    'default',
    'allow',
    1,
    1000 + number,
    cast(printf('{"orphan":%d}', number) as blob),
    case when number = 1 then 1 else 0 end,
    case when number = 1 then 1 else 0 end
from orphan;

insert into gate_evaluation_layers values (
    'eval-orphan-01',
    0,
    null,
    'inference',
    'orphan-layer',
    'complete',
    'match',
    'allow',
    'normalized',
    cast('{"input":"orphan"}' as blob),
    'orphan-layer-input-hash',
    'orphan-layer-output-hash',
    cast('{"output":"orphan"}' as blob),
    cast('{"metadata":"orphan"}' as blob),
    '2026-05-09T20:01:00Z',
    '2026-05-09T20:01:01Z',
    501,
    'orphan-service',
    'orphan-service-version',
    'orphan-model',
    'orphan-model-version',
    'orphan-prompt-hash',
    'orphan-schema-hash',
    'miss',
    'orphan-cache-key',
    7,
    '2026-05-10T20:01:01Z',
    'orphan-error-code',
    'orphan layer error',
    3
);

insert into gate_evaluation_labels values (
    'eval-orphan-01',
    'operator',
    1,
    'allow',
    'review',
    0.75,
    'orphan label rationale',
    '2026-05-09T20:01:02Z'
);
