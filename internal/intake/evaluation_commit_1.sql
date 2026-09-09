update intake_deferred
		set claim_owner = ?, claim_expires_at = ?,
			claim_attempt = claim_attempt + 1,
			last_replay_at = ?, replay_count = replay_count + 1
		where receipt_id = ? and state = ?
			and (claim_owner is null or claim_expires_at is null or claim_expires_at <= ?)
			and (next_attempt_at is null or next_attempt_at <= ?)
