select (select foreign_keys from pragma_foreign_keys),
       (select journal_mode from pragma_journal_mode),
       (select synchronous from pragma_synchronous),
       (select timeout from pragma_busy_timeout);
