insert into intake_deferred (
			receipt_id, event_id, state, pending_at, completed_at,
			last_replay_at, replay_count, claim_owner, claim_expires_at, claim_attempt
		) values (?, ?, ?, ?, null, cast(null as text), 0, null, cast(null as text), 0)
		on conflict(receipt_id) do update set
			state = excluded.state,
			pending_at = coalesce(intake_deferred.pending_at, excluded.pending_at),
			completed_at = null,
			claim_owner = null,
			claim_expires_at = null
