insert into intake_deferred (
				receipt_id,
				event_id,
				state,
				pending_at,
				completed_at,
				last_replay_at,
				replay_count
			) values (?, ?, ?,
				null,
				?,
				null,
				0)
			on conflict(receipt_id) do update set
				state = excluded.state,
				completed_at = excluded.completed_at
