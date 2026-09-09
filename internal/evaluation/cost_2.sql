select cache_status, count(distinct cache_key_hash)
		from gate_evaluation_layers
		where kind = 'inference'
			and cache_status in ('hit', 'miss')
			and (? = '' or completed_at >= ?)
			and (? = '' or completed_at <= ?)
		group by cache_status
