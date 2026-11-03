create trigger retry_failure before update on intake_deferred
begin
    select retry_cancel();
    select sum(value) from (
        with recursive counter(value) as (
            select 1 union all select value + 1 from counter where value < 100000000
        ) select value from counter
    );
end;
