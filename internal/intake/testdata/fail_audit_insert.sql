create trigger fail_audit_insert
before insert on events
when new.event_id = 'evt_blocked'
begin
    select raise(abort, 'forced audit failure');
end;
