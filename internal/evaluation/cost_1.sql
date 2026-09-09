with calls as (
			select
				request_id,
				max(coalesce(nullif(model_name, ''), requested_model)) as model_name,
				max(prompt_tokens) as prompt_tokens,
				max(completion_tokens) as completion_tokens,
				max(cached_tokens) as cached_tokens,
				min(completed_at) as first_at
			from gate_evaluation_layers
			where kind = 'inference'
				and upstream_metadata_status = 'present'
				and request_id != ''
				and coalesce(nullif(model_name, ''), requested_model, '') != ''
				and (? = '' or completed_at >= ?)
				and (? = '' or completed_at <= ?)
			group by request_id
		)
		select model_name, substr(first_at, 1, 10) as day, count(*) as calls,
			coalesce(sum(prompt_tokens), 0), coalesce(sum(completion_tokens), 0),
			coalesce(sum(cached_tokens), 0), min(first_at), max(first_at)
		from calls
		group by model_name, day
		order by day, model_name
