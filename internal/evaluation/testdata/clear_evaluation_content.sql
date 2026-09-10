update gate_evaluation_labels set rationale = null;
 update gate_evaluation_layers set input_json = null, output_json = null, metadata_json = null, error_message = null;
 update gate_evaluations set content_recorded = 0, error_json = null;
