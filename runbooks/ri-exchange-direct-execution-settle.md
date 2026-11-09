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
Resolving a row records what you found at AWS and releases its RIs. The row keeps its
original `status`; the resolution is stored beside it (`resolved_status`, `resolved_by`,
`resolved_at`, `resolution_note`), and rows with a resolution no longer block anything.

## Step 1: confirm the outcome at AWS

Run `DescribeReservedInstances` (and check the target offering) for the source RIs
named in the refusal, in the row's region. Decide:

- no exchange happened: resolve as `failed`;
- the exchange happened: resolve as `completed`.

Do not guess. Resolving `failed` releases the RIs for another exchange. Note the time you
ran the check: the resolve asks for it (`checked_at`).

## Step 2: list the stuck rows (API, primary path)

You need `execute:ri-exchange` on the row's account and region, as a signed-in user or a
user API key (the admin API key is refused). Placeholders: `<host>`, `<session-token>`,
`<csrf-token>`, `<id>`.

```sh
curl -s -H "Authorization: Bearer <session-token>" \
  "https://<host>/api/ri-exchange/direct-executions?status=unknown&region=<region>&ri_id=<ri-id>"
```

All query parameters are optional (`status` is `submitted` or `unknown`, default both;
`limit` 1 to 200; `cursor` from `next_cursor`). Each item has `id`, `status`,
`source_ids`, `created_at`, `error_summary`, `resolvable` and `resolvable_at`. Resolved
rows are not listed. A row outside your allowed accounts is not listed either.

## Step 3: resolve the row (API, primary path)

```sh
curl -s -X POST \
  -H "Authorization: Bearer <session-token>" -H "X-CSRF-Token: <csrf-token>" \
  -H "Content-Type: application/json" \
  "https://<host>/api/ri-exchange/direct-executions/<id>/resolve" \
  -d '{
    "outcome": "failed",
    "expected_status": "unknown",
    "reason": "DescribeReservedInstances shows the RIs still active, no exchange",
    "acknowledge_unknown_outcome": true,
    "cloud_check": {"checked_region": "<region>", "checked_at": "<RFC 3339 time of your check>"}
  }'
```

- `expected_status` is the status you saw in the list. If the row changed meanwhile you get 409.
- `outcome: "failed"` needs `acknowledge_unknown_outcome: true`. `completed` does not.
- `reason` is 10 to 300 characters, no control characters, and the stored note must fit 1000 bytes.
  Non-ASCII text needs fewer characters; the API answers 400 "reason too long once encoded; shorten it".
- `checked_region` must be the row's region. A row with an empty region cannot be resolved through the
  API (400 on region); use the break-glass statement below.
- A repeat of the same request returns the stored result with `replayed: true`. A repeat that
  differs in any field, including `expected_status`, is a 409, not a replay.
- 409 with "wait until" and a time means the row is too young; see "Why the age" below.
- 400 "checked_at must be at or after" and a time means your AWS check is too early; see
  "Why checked_at has a lower bound" below. Check again at AWS, then retry with the new time.

## Why the age

A `submitted` row can only be resolved 10 minutes after it was created. The claim and
the AWS call share one deadline of 4 minutes measured from just before the row is
saved, so a `submitted` row younger than 10 minutes (about 4 minutes plus 6 minutes of
margin for clock skew and finalize) may still be in flight. Resolving it `failed` would
release the RI while AWS might still commit the accept. `created_at` is taken with
`clock_timestamp()` after the guard lock is acquired, so a lock wait does not age the
row. An `unknown` row has no age rule: its accept already returned.

## Why checked_at has a lower bound

Your AWS check only counts if it was made after the exchange could no longer change.
So `checked_at` must not be in the future, and must be at or after:

- for a `submitted` row: `created_at` plus 10 minutes (the point where the process
  that was running it is certainly finished);
- for an `unknown` row: `completed_at` plus 1 minute (the accept had returned, and the
  1 minute allows for EC2 `DescribeReservedInstances` being eventually consistent).

A check made earlier proves nothing in either direction. The list shows `resolvable_at`,
which is that same earliest time, so a check at or after it is accepted.

## LAST RESORT: settle the row in SQL

Use this only when the API itself is unavailable. It bypasses the API's permission and
account checks and its audit log line, so a second person should review it. Run it only
after Step 1, in the application database, with the same rules as the API.

Find the row id from the server log (search for "held by direct execute"), or by RI id
(set the psql variables first, for example
`psql -v ri=ri-0abc -v id=<uuid> -v outcome=failed -v expected_status=unknown -v operator=<your-name> -v checked_at='2025-01-01T12:00:00Z' -v note='checked at AWS'`):

```sql
SELECT id, status, created_at, completed_at
  FROM ri_exchange_direct_executions
 WHERE provider = 'aws' AND status IN ('submitted', 'unknown') AND resolved_at IS NULL
   AND EXISTS (SELECT 1 FROM unnest(source_ids) x
                WHERE lower(btrim(x, E' \t\n\r\f\x0B')) = lower(btrim(:'ri', E' \t\n\r\f\x0B')));
```

```sql
-- :'id' = the audit row id; :'expected_status' = the status the SELECT showed ('submitted' or 'unknown');
-- :'outcome' = 'failed' (AWS shows no exchange) or 'completed' (AWS shows it happened);
-- :'operator' = your name; :'checked_at' = when you checked AWS; :'note' = what you found
UPDATE ri_exchange_direct_executions
   SET resolved_status     = :'outcome',
       resolved_by_user_id = NULL,
       resolved_by         = :'operator',
       resolved_at         = now(),
       resolution_note     = v.n
  FROM (SELECT 'break-glass checked_at=' || to_char(:'checked_at'::timestamptz AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
               || ' note=' || :'note' AS n) v
 WHERE id = :'id'
   AND provider = 'aws'
   AND resolved_at IS NULL
   AND status = :'expected_status'
   AND status IN ('submitted', 'unknown')
   AND octet_length(v.n) <= 1000
   AND btrim(:'operator') <> '' AND btrim(:'note') <> ''
   AND (   (status = 'submitted' AND created_at <= now() - interval '10 minutes'
                                 AND :'checked_at'::timestamptz >= created_at + interval '10 minutes')
        OR (status = 'unknown'   AND :'checked_at'::timestamptz >= completed_at + interval '1 minute'))
   AND :'checked_at'::timestamptz <= now();
-- Expect exactly 1 row updated. 0 rows means: wrong id, already resolved, a status that changed since
-- the SELECT, an empty operator or note, a stored note (with its prefix) over 1000 bytes, a 'submitted' row younger than 10 minutes that may still be in
-- flight, or a checked_at that is too early or in the future.
-- Do NOT widen the WHERE clause; wait, check AWS again and re-run.
```

The status and `error` of the row are left as they were; the columns above are all five
that make up a resolution (the table rejects a partial one).
