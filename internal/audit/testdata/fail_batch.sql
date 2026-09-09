create trigger fail_batch before insert on events
when new.event_id = 'second'
begin
    select raise(abort, 'forced batch failure');
end;
