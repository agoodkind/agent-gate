update gate_evaluation_layers
		set metadata_json = json_set(metadata_json, '$.prompt', 'prohibited')
		where evaluation_id = ? and layer_index = 1
