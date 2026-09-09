update intake_deferred
		set state = ?, completed_at = ?, claim_owner = null, claim_expires_at = null
		where receipt_id = ? and event_id = ? and state = ?
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
