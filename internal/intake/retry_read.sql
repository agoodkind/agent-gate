select exists(select 1 from intake_deferred where receipt_id = ?);
