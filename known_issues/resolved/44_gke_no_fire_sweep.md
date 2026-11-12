# Known Issues: GKE from the GCP environment root has no fire sweep

> **Audit status (2026-11-12):** `1 resolved (accepted and documented)`

## RESOLVED (accepted and documented): delayed purchases are refused on GKE from terraform/environments/gcp (#1767)

**Files:** `terraform/environments/gcp/compute.tf`, `terraform/modules/compute/gcp/gke/scheduled-tasks.tf`

**Description:** The GCP environment root passes no `enable_scheduled_tasks` and no `app_url` to the GKE module, so no fire sweep is scheduled. The API then reports `FIRE_SWEEP_SCHEDULED=false` and refuses delayed approvals (409) and a non-zero `purchase_delay_hours` (400). Enabling it needs a Cloud Scheduler URL that nobody has chosen for this root.

**Decision (owner, 2026-11-12):** leave GKE fire sweeps off and document it. See the "Deployments without a fire sweep" section of `docs/DEPLOYMENT.md`.

**Status:** Resolved by documentation (#1767). Reopen if a Cloud Scheduler URL is chosen for this root.
