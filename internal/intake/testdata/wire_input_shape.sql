select typeof(wire_input), length(wire_input)
		from intake_events
		where event_id = ?
