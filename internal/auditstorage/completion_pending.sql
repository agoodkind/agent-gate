select exists(select 1 from intake_deferred indexed by deferred_due_idx where state = 'pending')
