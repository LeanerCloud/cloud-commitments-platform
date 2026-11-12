-- Drop tracker issue references from the seeded Purchaser and RI Exchanger
-- descriptions (#1732). Each row is matched by its system-group id AND its
-- exact seeded text, so an admin-customised description is left alone.
UPDATE groups
SET description = 'Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties).'
WHERE id = '00000000-0000-5000-8000-000000000007'
  AND description = 'Execute, approve, and retry purchases. Membership is required even for admins to spend money (separation of duties, issue #923).';

UPDATE groups
SET description = 'Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties).'
WHERE id = '00000000-0000-5000-8000-000000000008'
  AND description = 'Execute RI exchanges. Membership is required even for admins, because an exchange consumes existing commitments and cannot be rolled back (separation of duties, issues #923 and #1644).';
