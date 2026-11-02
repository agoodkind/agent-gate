select
    (select count(*) from intake_receipts),
    (select count(*) from intake_deferred where state = 'complete'),
    (select count(*) from intake_deferred where state = 'pending'),
    (select count(*) from gate_evaluations);
