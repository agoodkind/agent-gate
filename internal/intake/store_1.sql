insert into intake_events (
			event_id,
			schema_version,
			recorded_at,
			system,
			session_id,
			turn_id,
			event_name,
			tool_name,
			tool_use_id,
			cwd,
			effective_cwd,
			command,
				file_path,
				raw_payload_hash, wire_input, normalized_input, provider_evidence,
				environment_evidence, recorded_detail_mask
			) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			on conflict(event_id) do nothing
