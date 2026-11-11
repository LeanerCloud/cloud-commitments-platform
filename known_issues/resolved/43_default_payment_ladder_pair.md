# Known Issues: default payment option and the ladder's conversion pair

> **Audit status (2026-11-11):** `0 needs triage · 1 resolved`

## LOW: the no-upfront default moves the ladder's global conversion pair (#1942)

**Files:** `internal/database/postgres/migrations/000166_default_payment_no_upfront.up.sql`, `internal/server/handler_ladder.go` (`handleLadderRun`, `ladderGlobalTerms`)

**Description:** Migration 000166 (#1758) changes the default payment option from all-upfront to no-upfront, and moves the seeded `global_config` row only when no admin ever saved settings. The ladder reads that row's term and payment as its global conversion pair, so on an untouched deployment the pair changes from (3yr, all-upfront) to (3yr, no-upfront). Sizing stays on the plan pair; only the base rate used to credit existing coverage rises, so for layers without an observed rate the ladder can size up where the account still holds older all-upfront commitments. This was named as an affected consumer in #1758 and is intended, but it changes sizing, so operators with laddering enabled and an untouched settings row should check the next plan before approving it. Deployments that saved their settings keep their value.

**Why deferred:** Not deferred. The shift is intended per #1758 and only needed documenting.

**Status:** Resolved in #1942. The behavior is intended; the migration comment now names the effect for operators. Reopen only if the owner wants the ladder's pair decoupled from the global default.
