update intake_deferred
		set claim_expires_at = ?
		where receipt_id = ? and event_id = ? and state = ?
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
