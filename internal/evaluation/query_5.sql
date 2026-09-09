select namespace, label_version, verdict, source, confidence, created_at
		from gate_evaluation_labels
		where evaluation_id = ?
		order by namespace, label_version
