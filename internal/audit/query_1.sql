select e.event_id, e.schema_version, e.time, e.level, e.message, e.system, e.session_id, e.turn_id, e.event_name, e.tool_use_id, e.tool_name, e.raw_payload_hash,
		coalesce(o.cwd, ''), coalesce(o.effective_cwd, ''), coalesce(o.command, ''), coalesce(o.file_path, ''),
		coalesce(d.kind, ''), coalesce(d.can_block, 0), coalesce(d.rules_checked_json, '[]'), coalesce(d.rules_matched_json, '[]')
from events e
left join operations o on o.event_id = e.event_id
left join decisions d on d.event_id = e.event_id
