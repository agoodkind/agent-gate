select e.event_id, e.recorded_at, e.system, e.session_id, e.turn_id,
    e.event_name, e.tool_name, e.tool_use_id, e.cwd, e.effective_cwd,
    e.command, e.file_path, e.raw_payload_hash,
    case when {{normalized}} and e.recorded_detail_mask & 2 != 0 then e.normalized_input end,
    case when e.recorded_detail_mask & 4 != 0 then e.provider_evidence end,
    case when {{environment}} and e.recorded_detail_mask & 8 != 0 then e.environment_evidence end,
    e.recorded_detail_mask,
    (case when e.wire_input is not null then 1 else 0 end |
    case when e.normalized_input is not null then 2 else 0 end |
    case when e.provider_evidence is not null then 4 else 0 end |
    case when e.environment_evidence is not null then 8 else 0 end),
    coalesce(d.state, ?), d.pending_at, d.completed_at, d.last_replay_at,
    coalesce(d.replay_count, 0)
from intake_events e
left join intake_receipts r on r.receipt_id = (
    select max(receipt_id) from intake_receipts where event_id = e.event_id)
left join intake_deferred d on d.receipt_id = r.receipt_id
