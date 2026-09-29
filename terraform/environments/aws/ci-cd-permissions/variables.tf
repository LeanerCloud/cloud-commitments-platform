variable "aws_region" {
  description = "AWS region for the provider and state backend"
  type        = string
  default     = "us-east-1"
}

variable "trust_principal" {
  description = "IAM principal allowed to assume the deploy role for local deployments, relative to the account ARN (e.g. 'user/alice', 'role/AdminRole'). Leave empty if only GitHub Actions OIDC is needed."
  type        = string
  default     = ""
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
