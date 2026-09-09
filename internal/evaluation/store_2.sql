insert into gate_evaluation_layers (
			evaluation_id, layer_index, parent_layer_index, kind, name, status, outcome, verdict,
			input_reference, input_hash, output_hash, started_at, completed_at,
			latency_us, service_name, service_version,
			model_name, model_version, prompt_hash, schema_hash, cache_status,
			cache_key_hash, cache_entry_version, cache_expires_at, error_code,
			retry_count, rule_name, checked_rules_json, upstream_metadata_status,
			request_id, requested_model, prompt_tokens, cached_tokens, completion_tokens,
 input_json, output_json, metadata_json, error_message
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
