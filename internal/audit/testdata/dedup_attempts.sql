create table write_attempts (event_id text not null);
create trigger count_write_attempts before insert on events begin
    insert into write_attempts values (new.event_id);
end;
