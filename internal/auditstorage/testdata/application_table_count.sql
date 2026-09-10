select count(*) from sqlite_schema where type = 'table' and name not like 'sqlite_%';
