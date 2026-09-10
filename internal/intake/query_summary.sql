select e.event_id, e.recorded_at, e.seq from intake_events e
left join intake_receipts r on r.receipt_id = (
    select max(receipt_id) from intake_receipts where event_id = e.event_id)
left join intake_deferred d on d.receipt_id = r.receipt_id
