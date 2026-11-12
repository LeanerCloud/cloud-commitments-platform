# Known Issues: Azure revoke "already returned" error code

> **Audit status (2026-11-12):** `1 needs triage · 0 resolved`

## MEDIUM: Azure "already returned" error code is unverified

**Files**: `internal/api/handler_purchases_revoke_uncertain.go` (`azureAlreadyReturnedCode`, `isAzureAlreadyReturned`), `internal/api/handler_purchases_revoke.go` (`handleAzureReturnError`)
**Description**: A single-send 4xx Return whose `ErrorCode` equals `ReservationAlreadyReturned` is mapped to a revoked purchase instead of a cleared flag (#1584). That string is only the name test fixtures have used; the Azure REST docs and `armreservations` v1.1.0 do not list it, so it has not been confirmed against a live or recorded Azure response. A code that never matches is a no-op (the refusal keeps its definite-refusal handling). Matching is by error code only, never by message text.
**Impact**: If Azure reuses the code for a "not returnable" refusal, a purchase would be marked revoked with no refund once the real code is confirmed and the constant corrected.
**Why deferred**: Needs a live Azure account or a recorded response; Azure deploys are paused.
**Status:** Open. Tracked in LeanerCloud/cloud-commitments-platform#1953 (part of #1584). Confirm the code against a live or recorded response, add that capture as a test fixture, and check that it is not shared with other refusals.
