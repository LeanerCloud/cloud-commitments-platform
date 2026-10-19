# Known Issues: Redshift reserved-node dedupe window

> **Audit status (2026-10-20):** `2 needs triage · 0 resolved`

## MEDIUM: a repeat same-tuple Redshift purchase within ~5 minutes adopts the earlier node without buying

**Files:** `internal/purchase/execution.go` (`executeSinglePurchase`), and in `LeanerCloud/cloud-commitments-go` the Redshift purchase dedupe in `providers/aws`

**Description:** The platform stamps `PurchaseOptions.RequestedAt` at dispatch. go's Redshift dedupe matches an existing reserved node through `DescribeReservedNodes` when its start time is at or after `RequestedAt - 5min`, and the window has no upper bound. A later purchase with the same account, region, offering and node count made within about 5 minutes of a completed one finds the earlier node, adopts it and reports success without buying anything.

**Why deferred:** The fix belongs in go (an upper bound or an identity stronger than the tuple), tracked in LeanerCloud/cloud-commitments-go#515. The platform only supplies `RequestedAt` (#702).

**Status:** Open. Tracked in #702 and LeanerCloud/cloud-commitments-go#515.

## LOW: unknown outcome followed by a new same-tuple purchase outside the window can double-buy

**Files:** `internal/purchase/execution.go`, `internal/purchase/manager.go`

**Description:** If a Redshift purchase ends with an unknown outcome and a NEW purchase (not a re-drive) of the same tuple is dispatched after the match window has passed, the dedupe cannot see the first node and both are bought. Redshift re-drive stays refused (`awsPurchaseHasNoDuplicateGuard`, `recRedriveRefusalReason`, `armedRedriveRefusal` are unchanged), so only a fresh purchase can hit this.

**Why deferred:** Mitigated by purchase suppressions and the operator message shown for an unknown outcome. A complete fix needs a durable per-purchase identity on the Redshift side.

**Status:** Open. Tracked in #702.
