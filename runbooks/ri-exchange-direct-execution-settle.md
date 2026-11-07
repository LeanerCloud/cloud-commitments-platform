# Runbook: settle a stuck direct RI exchange audit row

A direct RI exchange (`POST` execute) writes a row in `ri_exchange_direct_executions`
before it calls AWS. While the row is `submitted` or `unknown`, its source RIs are
held: no further direct execute, AUTO exchange or approval may take them (issue 1829),
because the exchange may already have happened at AWS.

- `submitted`: the process died, or the terminal write failed, before the outcome was recorded.
- `unknown`: the accept call failed without proving a rejection (the library's
  `ErrOutcomeUnknown`: a 5xx, timeout or transport error after the request may have
  reached AWS). On AWS, `unknown` means only that; a plain pre-accept error is
  recorded `failed`. Migration 000131's wording on `unknown` is stale for AWS.

The refusal the operator sees names the shared RIs and says to verify the outcome at AWS.

## Step 1: confirm the outcome at AWS

Run `DescribeReservedInstances` (and check the target offering) for the source RIs
named in the refusal. Decide:

- no exchange happened: settle as `failed`;
- the exchange happened: settle as `completed`.

Do not guess. Marking a row `failed` releases the RIs for another exchange.

## Step 2: find the audit row id

The refusal hides the blocking row's id. Find it in the server log (search for
"held by direct execute"), or query by the RI id named in the refusal (`:ri`):

```sql
SELECT id, status, created_at
  FROM ri_exchange_direct_executions
 WHERE provider = 'aws' AND status IN ('submitted', 'unknown')
   AND EXISTS (SELECT 1 FROM unnest(source_ids) x
                WHERE lower(btrim(x, E' \t\n\r\f\x0B')) = lower(btrim(:ri, E' \t\n\r\f\x0B')));
```

## Step 3: settle the row (the interim DBA step)

This rewrites rows that the audit design otherwise treats as final. It is the
interim until the resolve path (#1836) exists. Run only after Step 1.

```sql
-- :id = the audit row id; :new_status = 'failed' (AWS shows no exchange) or 'completed' (AWS shows it happened)
UPDATE ri_exchange_direct_executions
   SET status       = :new_status,
       completed_at = COALESCE(completed_at, now()),
       error        = concat_ws(E'\n', error, 'operator-settled ' || now()::text || ': ' || :note)
 WHERE id = :id
   AND provider = 'aws'
   AND status IN ('submitted', 'unknown')
   AND (status = 'unknown' OR created_at < now() - interval '5 minutes');
-- Expect exactly 1 row updated. 0 rows means: wrong id, already settled, or a 'submitted' row younger
-- than 5 minutes that may still be in flight. Do NOT widen the WHERE clause; wait and re-check.
```

- **Why the age check:** the AWS call can run for 4 minutes (`riExchangeExecutionTimeout`),
  so a `submitted` row younger than 5 minutes (4 minutes plus 1 minute of margin) may
  still be in flight. Marking it `failed` would release the RI while AWS might still
  commit the accept, and the handler's own finalize would then lose the real outcome.
- **Why `created_at` can be trusted:** it is taken with `clock_timestamp()` after the
  guard lock is acquired, so a lock wait does not age the row. A `submitted` row older
  than 5 minutes is past the execution window plus the claim.
- **Why `error` is appended:** it keeps the original failure text.
- An `unknown` row is settled at any age: its accept already returned.
