update intake_events
set wire_input = case when recorded_detail_mask & 1 != 0 then wire_input end,
    normalized_input = case when recorded_detail_mask & 2 != 0 then normalized_input end,
    provider_evidence = case when recorded_detail_mask & 4 != 0 then provider_evidence end,
    environment_evidence = case when recorded_detail_mask & 8 != 0 then environment_evidence end
where event_id = ? and not exists (
    select 1 from intake_receipts receipt
    where receipt.event_id = intake_events.event_id and (
        not exists (select 1 from gate_evaluations hot
            where hot.receipt_id = receipt.receipt_id and hot.mode = 'hot')
        or exists (select 1 from intake_deferred deferred
            where deferred.receipt_id = receipt.receipt_id and deferred.state = 'pending')
    )
);
