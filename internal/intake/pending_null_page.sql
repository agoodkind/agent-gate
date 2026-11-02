select receipt_id, state, next_attempt_at, claim_owner, claim_expires_at
from intake_deferred indexed by deferred_due_idx
where state = 'pending' and next_attempt_at is null
    and receipt_id > ? and receipt_id <= ?
order by receipt_id
limit 100
