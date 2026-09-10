select input_json, output_json, metadata_json, error_message
		from gate_evaluation_layers
		where evaluation_id = ? and layer_index = 1
