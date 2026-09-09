select e.event_id, e.time from events e
left join decisions d on d.event_id = e.event_id
