select
  (select count(*) from intake_receipts),
  (select count(*) from intake_deferred where state = 'complete'),
  (select count(*) from gate_evaluations where mode in ('deferred', 'deferred_replay')),
  (select count(*) from events where message = 'hook.audit_violation');
