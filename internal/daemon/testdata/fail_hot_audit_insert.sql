CREATE TRIGGER fail_hot_audit BEFORE INSERT ON events
WHEN NEW.message = 'hook.blocked'
BEGIN
    SELECT RAISE(ABORT, 'forced hot audit failure');
END;
