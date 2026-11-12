UPDATE groups
SET description = 'Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties, issue #923).'
WHERE id = '00000000-0000-5000-8000-000000000007'
  AND description = 'Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties).';

UPDATE groups
SET description = 'Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties, issues #923 and #1644).'
WHERE id = '00000000-0000-5000-8000-000000000008'
  AND description = 'Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties).';
