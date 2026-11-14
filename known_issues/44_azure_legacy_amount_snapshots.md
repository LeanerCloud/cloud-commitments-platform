# Known Issues: Azure legacy amounts already written to snapshots and rollups

> **Audit status (2026-11-05):** `1 needs triage · 0 resolved`

## MEDIUM: savings_snapshots and rollups written before the flag keep the pre-fix Azure amounts

**Files:** `internal/analytics/collector.go` (`aggregatePurchases`), `internal/database/postgres/migrations/000172_purchase_history_amount_basis.up.sql`, `runbooks/azure-legacy-amount-basis.md`

**Description:** Migration 000172 flags pre-fix Azure `purchase_history` rows with `amount_basis = 'legacy_window'`, and the dashboard, History summary, analytics, collector and Coverage totals now skip their dollars. Rows the collector wrote to `savings_snapshots` before the flag, and the rollups built from them, were computed from the estimated amounts and are not rewritten. New snapshots exclude the legacy dollars (the collector still counts the rows) while old ones keep them, so trend charts built from both step down at the deploy.

Also not changed: nothing writes `amount_basis` on new rows in stage 1, and the flag is set once by the migration. A row first stored after the migration but priced by pre-fix code (an old deploy still running) is not flagged.

**Why deferred:** Rewriting snapshots needs a source for the true amounts, which stage 2 of #669 (a backfill into corrected columns) provides. Rewriting history to zero would be wrong in the other direction.

**Status:** Open. Part of #669.
