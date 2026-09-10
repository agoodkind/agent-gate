select g.evaluation_id, g.receipt_id, g.event_id, g.attempt, g.mode,
    e.system, e.session_id, e.event_name, e.tool_name,
    g.config_hash, g.engine_version, g.engine_commit, g.engine_build_hash,
    g.input_hash, g.started_at, g.completed_at, g.final_verdict,
    g.final_source, g.enforcement_action, g.enforced, g.total_latency_us,
    g.layer_count, g.label_count, g.content_recorded,
    case when {{complete}} then 1 else 0 end
from gate_evaluations g
join intake_events e on e.event_id = g.event_id
