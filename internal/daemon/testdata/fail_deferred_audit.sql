create trigger fail_deferred_audit before insert on events
when new.message = 'hook.audit_violation'
begin select raise(abort, 'injected deferred audit failure'); end;
