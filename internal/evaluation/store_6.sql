select l.namespace, l.label_version, l.verdict, l.source, l.confidence,
			coalesce(l.rationale, ''), l.created_at
		from gate_evaluation_labels l
		where l.evaluation_id = ?
		order by l.namespace, l.label_version
