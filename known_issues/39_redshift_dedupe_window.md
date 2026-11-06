# Known Issues: Redshift reserved-node dedupe window

> **Audit status (2026-10-20):** `5 needs triage · 0 resolved`

## MEDIUM: a repeat same-tuple Redshift purchase within ~5 minutes adopts the earlier node without buying

**Files:** `internal/purchase/execution.go` (`executeSinglePurchase`), and in `LeanerCloud/cloud-commitments-go` the Redshift purchase dedupe in `providers/aws`

**Description:** The platform stamps `PurchaseOptions.RequestedAt` at dispatch. go's Redshift dedupe matches an existing reserved node through `DescribeReservedNodes` when its start time is at or after `RequestedAt - 5min`, and the window has no upper bound. A later purchase with the same account, region, offering and node count made within about 5 minutes of a completed one finds the earlier node, adopts it and reports success without buying anything.

**Why deferred:** The full fix belongs in go (an upper bound or an identity stronger than the tuple), tracked in LeanerCloud/cloud-commitments-go#515. The platform mitigates it for buys it records (below).

**Mitigation (platform-recorded buys):** The Redshift dispatch gate now refuses a purchase when another lineage's completed, partially completed or revocation-requested row for the same tuple was last saved within 10 minutes of the new lineage's first dispatch time. The refusal is recorded before submission with a fixed text that names no execution, and the user starts a new purchase at least 10 minutes after the other one completed.

**Remaining:**

- Buys made outside the platform are invisible to the gate.
- Two identical-tuple recs in one execution still adopt each other (platform #1809).
- A lineage whose purchase record never landed (the child bought but its row save failed, `execution.go:351` and `:383`) leaves no completed row for the gate to see.
- The go-side narrowing in LeanerCloud/cloud-commitments-go#700 and the real fix in #515 are open.
- The window is measured from the other row's last save, so a later save of that row (for example a revocation request) restarts it. That only refuses longer; it never allows a double buy.
- A `failed` row without the no-submission proof (reaped mid-buy, or an unknown outcome) may hold a real node but is not refused by this window; if go adopts that node, the accounting stays one node and one completed row. PR3 counts such rows as open lineages.
- After the Redshift re-drive guard is lifted, a retry can be refused for up to 24h while another lineage's completed node sits inside its window (liveness only, no double buy).

**Status:** Mitigated for platform-recorded buys. Tracked in #702 and LeanerCloud/cloud-commitments-go#515.

## LOW: unknown outcome followed by a new same-tuple purchase outside the window can double-buy

**Files:** `internal/purchase/execution.go`, `internal/purchase/manager.go`

**Description:** If a Redshift purchase ends with an unknown outcome and a NEW purchase (not a re-drive) of the same tuple is dispatched after the match window has passed, the dedupe cannot see the first node and both are bought. Redshift re-drive stays refused (`awsPurchaseHasNoDuplicateGuard`, `recRedriveRefusalReason`, `armedRedriveRefusal` are unchanged), so only a fresh purchase can hit this.

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

## LOW: RequestedAt is stamped per dispatch, not per purchase lineage

**Files:** `internal/purchase/execution.go`

**Description:** The library documents `RequestedAt` as stable across re-drives of the same token; the platform stamps a fresh time per dispatch. This is only safe because Redshift re-drive is refused. Lifting that refusal requires a persisted first-dispatch time (#702 option B).

**Why deferred:** Re-drive stays refused, so there is no current impact.

**Status:** Open. Tracked in #702.
