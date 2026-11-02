select
  (select count(*) from gate_evaluations where mode in ('deferred', 'deferred_replay')),
  (select count(*) from gate_evaluations where mode in ('deferred', 'deferred_replay') and content_recorded != 0),
  (select count(*) from gate_evaluation_layers layer join gate_evaluations evaluation using (evaluation_id)
     where evaluation.mode in ('deferred', 'deferred_replay') and
       (layer.input_json is not null or layer.output_json is not null or layer.metadata_json is not null)),
  (select content_recorded from gate_evaluations where receipt_id = 1 and mode = 'hot'),
  (select content_recorded from gate_evaluations where receipt_id = 2 and mode = 'hot');
