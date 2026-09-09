select receipt_id
from intake_deferred
where state = 'pending' and receipt_id > ?
    and (next_attempt_at is null or next_attempt_at <= ?)
    and (claim_owner is null or claim_expires_at is null or claim_expires_at <= ?)
order by receipt_id
limit ?
