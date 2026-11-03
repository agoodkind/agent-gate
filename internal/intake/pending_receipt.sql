select receipt_id, state, next_attempt_at, claim_owner, claim_expires_at
from intake_deferred where receipt_id = ? and state = 'pending'
