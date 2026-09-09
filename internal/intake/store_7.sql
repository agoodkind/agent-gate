update intake_deferred
			set last_replay_at = ?, replay_count = replay_count + 1
			where receipt_id = ? and state = ?
