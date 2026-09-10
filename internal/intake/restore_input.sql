update intake_events
set wire_input = coalesce(wire_input, ?),
    normalized_input = coalesce(normalized_input, ?),
    provider_evidence = coalesce(provider_evidence, ?),
    environment_evidence = coalesce(environment_evidence, ?),
    recorded_detail_mask = recorded_detail_mask | ?
where event_id = ? and raw_payload_hash = ?;
