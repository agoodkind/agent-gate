update intake_deferred
set claim_owner = null, claim_expires_at = null, next_attempt_at = ?
where receipt_id = ? and event_id = ? and state = 'pending'
    and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
