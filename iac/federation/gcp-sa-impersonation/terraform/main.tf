terraform {
  required_version = ">= 1.5"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 5.0"
    }
    http = {
      source  = "hashicorp/http"
      version = ">= 3.4"
    }
  }
}

# Grant the source SA permission to impersonate the target SA.
# Use _member (not _binding) to avoid replacing other existing bindings.
resource "google_service_account_iam_member" "cudly_impersonate" {
  service_account_id = "projects/${var.project_id}/serviceAccounts/${var.service_account_email}"
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = "serviceAccount:${var.source_service_account}"
}

# Grant the target SA permissions needed to manage CUDs/commitments.
#
# roles/commerceorgpolicy.commitmentAdmin and roles/billing.viewer are
# organization- and billing-account-scoped respectively; granting either at
# project scope via google_project_iam_member 400s (same failure mode
# terraform/modules/compute/gcp/cloud-run/main.tf:484 documents for
# billing.viewer, and federation/gcp-target/terraform/variables.tf:198-201
# documents for commitmentAdmin). Neither can work here.
#
# Instead grant exactly what CUDly needs, mirroring the sibling
# gcp-target bundle (iac/federation/gcp-target/terraform/main.tf) so the
# two onboarding paths converge on the same permission set: a custom role
# holding only the two commitment-write permissions, plus the built-in
# compute.viewer role for read access (regions/zones/machineTypes/commitments
# .list/.get). There is no billing-read path in this bundle (no
# billing_account_id input to scope roles/billing.viewer to), so it is
# dropped rather than reintroduced broken.
resource "google_project_iam_custom_role" "cudly" {
  project = var.project_id
  role_id = var.custom_role_id
  title   = "CUDly Commitment Writer"
  # Description matches terraform/modules/compute/gcp/cloud-run and
  # federation/gcp-target exactly so applies from any of the three don't
  # stomp each other.
  description = "Minimum permissions for CUDly to purchase and update committed use discounts."
  permissions = var.custom_role_permissions
  stage       = "GA"
}

resource "google_project_iam_member" "cudly_custom" {
  project = var.project_id
  role    = google_project_iam_custom_role.cudly.id
  member  = "serviceAccount:${var.service_account_email}"
}

resource "google_project_iam_member" "cudly_compute_viewer" {
  project = var.project_id
  role    = "roles/compute.viewer"
  member  = "serviceAccount:${var.service_account_email}"
}
