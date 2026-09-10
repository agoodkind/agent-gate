select
    count(*) - coalesce(sum(case when {{complete}} then 1 else 0 end), 0),
    min(case when {{complete}} then g.completed_at end)
from gate_evaluations g
join intake_events e on e.event_id = g.event_id
