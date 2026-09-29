variable "subscription_id" {
  description = "Azure subscription ID"
  type        = string
}

variable "github_repository_id" {
  description = "Immutable numeric ID of the GitHub repository whose Actions workflows may deploy (gh api repos/<owner>/<repo> --jq .id). Trust is keyed on this ID, not owner/name, so a rename or a new repo reusing a freed name neither gains nor loses deploy rights."
  type        = string

  validation {
    condition     = can(regex("^[1-9][0-9]*$", var.github_repository_id))
    error_message = "github_repository_id must be the repository's numeric ID (gh api repos/<owner>/<repo> --jq .id), not its owner/name."
  }
}

variable "github_repository_owner_id" {
  description = "Immutable numeric ID of the GitHub account that owns the repository (gh api repos/<owner>/<repo> --jq .owner.id)."
  type        = string

  validation {
    condition     = can(regex("^[1-9][0-9]*$", var.github_repository_owner_id))
    error_message = "github_repository_owner_id must be the owner's numeric ID (gh api repos/<owner>/<repo> --jq .owner.id), not its login."
  }
}

variable "github_environments" {
  description = <<-EOT
    GitHub deployment environment names whose jobs may authenticate via federated
    identity credentials. A job carrying `environment: <name>` presents the OIDC
    subject <prefix>:environment:<name> (see local.github_oidc_sub_prefix in
    sp.tf), so a name absent from this list
    cannot authenticate (AADSTS70021).

    NOTE the subject is ref-agnostic: it does not encode the branch, so a
    credential here lets ANY branch that can reach a job bound to that environment
    obtain the deploy service principal. Add a name only once a workflow actually
    presents it, and restrict the branch via the environment's
    deployment_branch_policy — an in-workflow ref check does not bind, because
    workflow_dispatch runs the file as it exists on the dispatched ref.

    This list is NOT a complete enumeration of the environment subjects this repo
    presents to Azure. It covers the destroy jobs bound by cleanup-staging.yml
    (`staging`) and destroy-fargate-dev.yml (`dev`), deploy-azure.yml's
    build-and-deploy and test-deployment jobs (see #140), and rollback.yml's
    rollback-azure job (see #139) — all of which bind plain
    `dev`/`staging`/`prod`.

    Knowingly NOT covered, tracked in #1648 — this Azure job binds a compound
    environment name and therefore still fails with AADSTS70021:
      - database-migration.yml -> azure-db-{dev,staging,prod}

    They are excluded here rather than fixed because each needs its environment
    created and gated first; minting a credential for an ungated environment that
    does not yet exist would widen access without adding a control. Do not read
    the absence of a name as "no job uses it".
  EOT
  type        = list(string)
  default     = ["dev", "staging", "prod"]

  validation {
    condition     = length(var.github_environments) == length(distinct(var.github_environments))
    error_message = "github_environments must not contain duplicates; toset() would silently collapse them, so a duplicate indicates a typo."
  }

  validation {
    condition     = alltrue([for e in var.github_environments : trimspace(e) != ""])
    error_message = "github_environments entries must be non-empty; an empty name yields the unmatchable subject <prefix>:environment:."
  }
}
