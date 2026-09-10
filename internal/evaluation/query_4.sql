select layer.layer_index, layer.parent_layer_index, layer.kind, layer.name,
    layer.status, layer.outcome, layer.verdict,
    input_reference, input_hash, output_hash,
    case when ? then output_json end,
    case when ? then metadata_json end,
    started_at, completed_at, latency_us, service_name, service_version,
    model_name, model_version, prompt_hash, schema_hash, cache_status,
    cache_key_hash, cache_entry_version, cache_expires_at, error_code, retry_count
from gate_evaluation_layers layer
where layer.evaluation_id = ? order by layer.layer_index
