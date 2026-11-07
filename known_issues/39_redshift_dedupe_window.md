# Known Issues: Redshift reserved-node dedupe window

> **Audit status (2026-11-06):** `5 needs triage · 1 resolved`

## MEDIUM: a repeat same-tuple Redshift purchase within ~5 minutes adopts the earlier node without buying

**Files:** `internal/purchase/execution.go` (`executeSinglePurchase`), and in `LeanerCloud/cloud-commitments-go` the Redshift purchase dedupe in `providers/aws`

**Description:** The platform stamps `PurchaseOptions.RequestedAt` at dispatch. go's Redshift dedupe matches an existing reserved node through `DescribeReservedNodes` when its start time is at or after `RequestedAt - 5min`, and the window has no upper bound. A later purchase with the same account, region, offering and node count made within about 5 minutes of a completed one finds the earlier node, adopts it and reports success without buying anything.

**Why deferred:** The full fix belongs in go (an upper bound or an identity stronger than the tuple), tracked in LeanerCloud/cloud-commitments-go#515. The platform mitigates it for buys it records (below).

**Mitigation (platform-recorded buys):** The Redshift dispatch gate now refuses a purchase when another lineage's completed, partially completed or revocation-requested row, or a failed row without the no-submission proof (settled or not, and whether or not Retry is refused), for the same tuple was last saved within 10 minutes of the new lineage's first dispatch time. The refusal is recorded before submission with a fixed text that names no execution, and the user starts a new purchase at least 10 minutes after the other one ended, or retries or settles it if it failed and can still be retried.

**Remaining:**

- Buys made outside the platform are invisible to the gate.
- Two identical-tuple recs in one execution still adopt each other (platform #1809).
- A lineage whose purchase record never landed (the child bought but its row save failed, `execution.go:351` and `:383`) leaves no completed row for the gate to see.
- The go-side narrowing in LeanerCloud/cloud-commitments-go#700 and the real fix in #515 are open.
- The window is measured from the other row's last save, so a later save of that row (for example a revocation request) restarts it. That only refuses longer; it never allows a double buy.
- A `failed` row without the no-submission proof (reaped mid-buy, or an unknown outcome) may hold a real node. Inside the window the gate refuses it like a finished row, settled or not and whether or not Retry is refused (#1839); outside it, the gate counts it as an open lineage only while Retry can still re-drive it (below).
- Now that the Redshift re-drive guard is lifted, a retry can be refused for up to 24h while another lineage's completed node sits inside its window (liveness only, no double buy).
- Plan rows that PR2b refused before submission (failed, `no_submission`, attempt 0) used to hold their ramp step until the account was detached. They are now retryable, so that hold clears with a Retry once the other node leaves its window.

**Status:** Mitigated for platform-recorded buys. Tracked in #702 and LeanerCloud/cloud-commitments-go#515.

**History:** #1839 extended the adoption-window refusal to failed rows that may have bought, including settled rows and rows Retry refuses for good.

## LOW: unknown outcome followed by a new same-tuple purchase outside the window can double-buy

**Files:** `internal/purchase/execution.go`, `internal/purchase/manager.go`

**Description:** If a Redshift purchase ends with an unknown outcome and a NEW purchase (not a re-drive) of the same tuple is dispatched after the match window has passed, the dedupe cannot see the first node and both are bought. A retry of the same purchase is guarded (it reuses the first dispatch time, see below), so only a fresh purchase can hit this.

**Why deferred:** Mitigated by purchase suppressions and the operator message shown for an unknown outcome. A complete fix needs a durable per-purchase identity on the Redshift side.

**Status:** Open. Tracked in #702.

## MEDIUM: concurrent or duplicate same-tuple Redshift purchases are not distinguished

**Files:** `internal/purchase/execution.go` (fan-out, ~650-690), `internal/purchase/manager.go`

**Description:** Two recs in one execution, or two executions, for the same account, region, offering ID and node count get different idempotency tokens, but the library matches by tuple only. Run sequentially, the second adopts the first node and reports success without buying. Run in parallel (recs in one execution run concurrently, and the per-row claim does not span executions), both guard lookups can see no node and both buy (check-then-act).

**Why deferred:** Needs a tuple-level lock or a stronger identity in go (cloud-commitments-go#515).

**Status:** Open. Tracked in #702.

## MEDIUM: adoption of an existing Redshift node is invisible

**Files:** `internal/purchase/execution.go`, `internal/purchase/manager.go`

**Description:** The platform does not read `PurchaseResult.ExistingCommitment`, so an adopted node (guard match or unknown-outcome recovery) is recorded as an ordinary successful purchase (history row, totals, Cost nil) with no flag that nothing was bought.

**Why deferred:** Surfacing adoption needs a history/UI field and is separate from stamping `RequestedAt`.

**Status:** Open. Tracked in #702.

## RESOLVED: RequestedAt is stamped per dispatch, not per purchase lineage

**Files:** `internal/purchase/redshift_lineage.go`, `internal/purchase/manager.go`, `internal/api/handler_purchases_redshift_retry.go`

**Description:** The library documents `RequestedAt` as stable across re-drives of the same token. The platform now persists the lineage's first dispatch time (`first_dispatch_at`), copies it to every retry successor, and dispatches under it, so a re-drive carries the original `RequestedAt`. The Redshift re-drive refusal is lifted behind the Retry gate (10 minute settle, 24h horizon, stamp required) and the dispatch gate.

**Status:** Resolved in #702.

## LOW: limits of the Redshift re-drive

**Files:** `internal/purchase/redshift_lineage.go`, `internal/purchase/redshift_retry.go`, `internal/config/store_postgres_redshift_lineage.go`, `internal/api/handler_history.go`

**Description:** Fail-closed edges that cost liveness, not money:

- A failed Redshift row that Retry can still re-drive (no successor, not settled, not a never-submitted attempt 0, first dispatched within 24h) counts as an open lineage. Two failed same-tuple lineages inside the horizon therefore block each other's new purchases and Retry for up to 24h, which includes the two lineages that exist at deploy time. Operator-fixable failures (`ResolveOpsHint`) and rows past the horizon do not count.
- Failed rows that carry a definite AWS refusal still count, because their first dispatch time is set. Retry them or wait out the horizon.
- A failed direct-purchase row cannot be settled (settling covers plan rows only), so it blocks the same tuple until it is retried or 24h pass.
- A retried row whose ladder tranche is already settled (`errRetryLadderTrancheSettled`) keeps its lineage open for up to 24h.
- The conflict query treats a NULL account on either side as matching any account, so it can over-refuse across accounts. It never under-refuses.
- Rows the reaper failed before anything was sent have no first dispatch time and are not `no_submission`, so Retry refuses them for good; start a new purchase.
- A failed attempt 1 row that is `no_submission` is a dead end: Retry treats only a never-submitted attempt 0 as skippable. Widening that to "the predecessor is `no_submission`" is unsafe, because `ladderExecutionRefusal` can mark a legacy attempt 1 successor `no_submission` while attempt 0 may have sent (`manager.go`, `preExecutionRefusal`); only a check that the whole chain is `no_submission` would be safe.
- Without an attempt journal (`ManagerConfig.AttemptJournal` nil) go keeps its window dedupe only, so the offering-ID hole and the listing-lag window stay open. A retry successor (attempt above 0) is then refused with a plain error and no `no_submission` proof, so each refused successor stays an open failed row and blocks same-tuple purchases for up to 24h.
- A stranded approved row is re-driven in place without a settle wait. That is safe because the sweep claims only `approved` rows, nothing writes `approved` after `running`, and so a stranded row was never claimed and never sent. A stranded successor above attempt 0 is covered by the Retry-time settle and the journal check.
- Legacy armed-refusal rows ("refusing to execute retry attempt N: AWS EC2 and Redshift ...") are still allowlisted for display. History now shows the permanent no-first-dispatch-time hint beside that stored text; both say the purchase cannot be retried.
- History computes the stamp age on the application clock as a hint, while the Retry gate reads the database clock. Close to 24h, Retry can show and then answer 409. The gate is authoritative.
- The History hint does not show the 10 minute settle wait; the Retry button answers 409 with `retry_after_seconds` instead.
- The console and `cudly-mcp` buyers were not checked for the stamp and journal wiring; only the platform dispatch path is covered.
- A child that bought but whose row save failed (`execution.go:351`, `:383`) leaves no row for the gate, unless its root is a retryable failed row.
- Same-family successors of one token (for example a root retry's fan-out child and a retried child, same key) now also wait out the 10 minute settle at dispatch (a sibling that failed after a possible send, or finished, within 10 minutes); the Retry-creation check alone did not cover two successors of one token.
- Related go work: LeanerCloud/cloud-commitments-go#700 (claimed reservations) and #515 (identity stronger than the tuple).

**Status:** Open. Tracked in #702.
