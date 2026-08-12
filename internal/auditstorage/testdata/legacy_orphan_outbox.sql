pragma foreign_keys = off;

insert into deferred_audit_outbox values (
    1001,
    'event-orphan-01',
    'eval-orphan-01',
    'complete',
    '2026-05-09T20:01:03Z',
    '2026-05-09T20:01:04Z',
    null,
    null,
    1
);

insert into deferred_audit_outbox_entries values (
    1001,
    0,
    'audit-orphan-01',
    x'00ff7b226f727068616e223a747275657d',
    '2026-05-09T20:01:04Z'
);
