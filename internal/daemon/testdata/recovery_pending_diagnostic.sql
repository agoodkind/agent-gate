select receipt_id, claim_attempt, coalesce(claim_owner, ''), coalesce(claim_expires_at, ''), coalesce(next_attempt_at, '') from intake_deferred where state = 'pending' order by receipt_id
