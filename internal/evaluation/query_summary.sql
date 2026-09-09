select g.evaluation_id, g.completed_at
from gate_evaluations g
join intake_events e on e.event_id = g.event_id
