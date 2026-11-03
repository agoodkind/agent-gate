create trigger retry_failure before update on intake_deferred
begin
    select raise(abort, 'retry update failed');
end;
