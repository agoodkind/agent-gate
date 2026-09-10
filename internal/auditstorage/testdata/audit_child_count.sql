select (select count(*) from operations) + (select count(*) from decisions) +
       (select count(*) from violations);
