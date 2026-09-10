SELECT count(*) FILTER (WHERE e.mode = 'hot'),
       count(*) FILTER (WHERE e.mode <> 'hot')
FROM gate_evaluation_layers AS l
JOIN gate_evaluations AS e ON e.evaluation_id = l.evaluation_id
WHERE l.kind = 'inference';
