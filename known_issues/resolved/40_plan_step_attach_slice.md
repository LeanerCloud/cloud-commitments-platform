# Known Issues: plan step attach slice

> **Audit status (2026-11-04):** `0 needs triage · 1 resolved`

## LOW: a root dated in the last 24h of the notice window may miss attachment on a daily tick

**Files:** `internal/purchase/notifications.go` (`shouldNotifyPlan`), `internal/purchase/plan_step_attach.go` (the `VetoWindowEnd` gate), `internal/api/handler_plans.go` (`requireStartDateOutsideNoticeMargin`), `internal/api/plan_health.go` (`stepWillNotAttachFactor`), `internal/config/store_postgres_doomed_root.go`, `terraform/modules/compute/aws/lambda/variables.tf` (`send_notifications_schedule`)

**Description:** The tick looks at a plan only when its date is less than N+1 days away (`notifications.go:66-68`, a floor-to-whole-days test on the plan's `NextExecutionDate`), and the attach gate adds recommendations only when the root's own date is at least N days away (`plan_step_attach.go:111`). Before the fix, a root dated D attached only from a tick inside the 24h slice (D-(N+1)d, D-Nd]. With a daily sweep that gave one chance, and a tick that fell outside the slice left the root empty until it failed on its date. The two tests also read different dates (the plan's next date against the root's date), so a plan whose date and root date have drifted apart can see the slice shifted.

Add Purchases now refuses a start date before now+(N+1)d (409 `start_date_inside_notice_margin`), which keeps the whole slice in the future for every new root. The plan-health factor `step_will_not_attach` flags only roots that are certainly doomed (dated before now+N, or any future empty root when N is 0). Roots already stored in [now+N, now+N+1d) are not flagged and are known to be at risk; they are not assumed safe.

Three things make a daily tick less certain than it looks. The sweep schedule is opt-in (`enable_send_notifications_schedule` defaults to false, `rate(1 day)` when enabled), so the cadence is a deployment default, not a guarantee. The 24h notification throttle (`MinHoursBetweenNotifications`, `notifications.go:74-79`) can skip the next daily tick after any notice sent to the same plan. And a modal opened before UTC midnight and submitted after it can get the 409 on its suggested date; the 409 text names the earliest valid date.

**Status:** Resolved. Tracked in #1755 (p3).

## Resolution

Fixed by commit d7986084 (#1755). The tick now also visits any plan whose next-step empty root's own date is in [now+N, now+N+2d), keyed on the root's own date, so a missed daily tick cannot strand a root. The N-day gate and the 24-hour throttle are unchanged. Covered by `TestTickAttachesEmptyRootByItsOwnDate`.
