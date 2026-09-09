select
			e.seq,
			e.event_id,
			e.schema_version,
			e.recorded_at,
			e.system,
			e.session_id,
			e.turn_id,
			e.event_name,
			e.tool_name,
			e.tool_use_id,
			e.cwd,
			e.effective_cwd,
			e.command,
			e.file_path,
				e.wire_input,
				e.raw_payload_hash,
				e.normalized_input,
				e.provider_evidence,
				e.environment_evidence,
			coalesce(r.receipt_id, 0),
			coalesce(r.received_at, e.recorded_at),
			coalesce(d.state, ?),
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			coalesce(d.replay_count, 0)
		from intake_events e
		left join intake_receipts r on r.receipt_id = (
			select max(receipt_id) from intake_receipts where event_id = e.event_id
		)
		left join intake_deferred d on d.receipt_id = r.receipt_id
		where e.event_id = ?
