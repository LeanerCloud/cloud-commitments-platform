variable "project_id" {
  description = "GCP project ID of the target account."
  type        = string
}

variable "service_account_email" {
  description = "Email of the service account in the target project that CUDly will use."
  type        = string
}

variable "source_service_account" {
  description = "Full email of the service account that CUDly runs as on the source GCP project."
  type        = string
}

variable "create_custom_role" {
  description = <<-EOT
    Whether to create the var.custom_role_id custom role in this project.
    Default true is correct when this bundle is the only onboarding path
    applied to the project.

    Set to false ONLY after confirming the role already exists (e.g. `gcloud
    iam roles describe var.custom_role_id --project var.project_id`):
    either terraform/modules/compute/gcp/cloud-run (self-hosted CUDly,
    unconditional) or federation/gcp-target (only when it created its own
    service account, i.e. its var.service_account_email was left empty) can
    have already created a role with the same ID in this project. If
    neither applies, the role does not exist and setting this to false
    makes the binding below target a missing role, which fails the apply.
  EOT
  type        = bool
  default     = true
}

variable "custom_role_id" {
  description = <<-EOT
    Project-scoped custom role ID Terraform creates and binds to
    var.service_account_email. The role carries the minimum permissions
    required to purchase and manage Compute Engine CUDs on behalf of CUDly.
    Matches the default in federation/gcp-target/terraform so applies from
    either bundle stay idempotent against the same project.
  EOT
  type        = string
  default     = "cudlyCommitmentWriter"
}

variable "custom_role_permissions" {
  description = <<-EOT
    Permissions bundled into the custom role granted to
    var.service_account_email. Defaults match the definition in
    terraform/modules/compute/gcp/cloud-run and federation/gcp-target, so all
    three stay in lockstep and none can stomp another's role on apply.

    Read-side permissions (regions/zones/machineTypes/commitments.list/.get)
    come from roles/compute.viewer, granted separately in main.tf.
  EOT
  type        = list(string)
  default = [
    "compute.commitments.create",
    "compute.commitments.update",
  ]
}

variable "cudly_api_url" {
  description = "CUDly API base URL for automatic account registration. Leave empty to skip registration."
  type        = string
  default     = ""
}

variable "account_name" {
  description = "Human-readable name for this account in CUDly."
  type        = string
  default     = ""
}

variable "contact_email" {
  description = "Contact email for registration notifications."
  type        = string
  default     = ""
}
