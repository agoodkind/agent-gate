select l.status, l.error_code from gate_evaluation_layers l
join gate_evaluations e on e.evaluation_id = l.evaluation_id
where e.mode <> 'hot' and l.kind = 'inference'
