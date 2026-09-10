select count(*) filter (where state = 'pending'),
       count(*) filter (where state = 'complete')
from intake_deferred
