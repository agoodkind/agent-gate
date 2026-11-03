select config_hash from gate_evaluations where mode in ('deferred', 'deferred_replay') order by receipt_id;
