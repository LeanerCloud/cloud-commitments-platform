# Known Issues: purchase delay default on deployments without the fire sweep

> **Audit status (2026-11-10):** `1 needs triage · 0 resolved`

## MEDIUM: a fresh install without the fire sweep returns 409 on every approval until the delay is set to 0

**Files:** `internal/api/handler_purchases.go` (`purchaseDelay`, 1193-1206), `internal/api/handler_config.go` (`validateGlobalConfigWrite`, 200-222; `refusesDelay`, 231-233), `internal/database/postgres/migrations/000070_purchase_history_revocation.up.sql`

**Description:** Migration 000070 now adds `purchase_delay_hours` with default 48 (#1934), so a fresh install has the documented free-cancel window. On a deployment with `FIRE_SWEEP_SCHEDULED=false` there is no sweep to carry out a delayed purchase, so a stored delay above 0 is refused with 409 on approval until it is set to 0. Installs that applied 000070 earlier keep their stored value and are unaffected. The settings page has no field for the value, so the admin must use `PUT /api/config` (or seed the row). The onboarding step is in `docs/DEPLOYMENT.md` ("New installs default to a 48 hour purchase delay").

**Why deferred:** An owner trade-off accepted in #1934: fail closed with a clear 409 rather than fail open with no cancel window. The 400 (PUT /api/config) vs 409 (approval) mismatch between the two refusals is tracked in #1937.

**Status:** Needs triage.
