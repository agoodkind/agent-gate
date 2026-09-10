select l.layer_index, l.parent_layer_index, l.kind, l.name, l.status,
			l.outcome, l.verdict, l.input_reference, l.input_json, l.input_hash,
			l.output_hash, l.output_json, l.metadata_json, l.started_at, l.completed_at,
			l.latency_us, l.service_name, l.service_version, l.model_name, l.model_version,
			l.prompt_hash, l.schema_hash, l.cache_status, l.cache_key_hash,
			l.cache_entry_version, l.cache_expires_at, l.error_code,
			coalesce(l.error_message, ''), l.retry_count
		from gate_evaluation_layers l
		where l.evaluation_id = ?
		order by l.layer_index
