create trigger block_dedup_write before insert on events begin
    select raise(abort, 'blocked audit write');
end;
