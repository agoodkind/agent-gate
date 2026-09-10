select g.evaluation_id, g.receipt_id, g.event_id, g.attempt, g.mode,
			g.config_hash, g.engine_version, g.engine_commit, g.engine_build_hash,
			g.input_hash, g.started_at, g.completed_at, g.final_verdict, g.final_source,
			g.enforcement_action, g.enforced, g.total_latency_us, g.error_json
		from gate_evaluations g
		where g.evaluation_id = ?
