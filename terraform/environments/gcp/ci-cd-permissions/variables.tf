variable "project_id" {
  description = "GCP project ID"
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

variable "deploy_ref" {
  description = "Git ref allowed to deploy (e.g. refs/heads/main). Only workflows from this exact ref can impersonate the deploy SA. PRs, tags, and other branches are blocked."
  type        = string
  default     = "refs/heads/main"
}
