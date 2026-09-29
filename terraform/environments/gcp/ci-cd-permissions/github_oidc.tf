resource "google_iam_workload_identity_pool" "github" {
  workload_identity_pool_id = "github-actions"
  display_name              = "GitHub Actions"
  description               = "Workload Identity Pool for GitHub Actions OIDC"
  project                   = var.project_id
}

moved {
  from = google_iam_workload_identity_pool.github[0]
  to   = google_iam_workload_identity_pool.github
}

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions"
  project                            = var.project_id

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }

  # Map GitHub token claims to Google attributes for use in conditions and bindings.
  # attribute.repository is kept for audit logs only; nothing grants on it.
  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.actor"               = "assertion.actor"
    "attribute.repository"          = "assertion.repository"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
    "attribute.ref"                 = "assertion.ref"
  }

  # Restrict to the specific repo AND only allow the designated deploy branch.
  # PRs, tags, forks, and workflow_dispatch from other refs are blocked. The repo
  # is matched on its immutable owner and repo IDs, not its name, so a rename or
  # a new repo reusing a freed name neither gains nor loses deploy rights.
  #
  # deploy_ref defaults to refs/heads/main. Override in terraform.tfvars only
  # when temporarily deploying from a feature branch; flip back to main once
  # that branch merges. Forgetting to flip locks out main-branch deploys with
  # "unauthorized_client: The given credential is rejected by the attribute condition."
  attribute_condition = "assertion.repository_owner_id == '${var.github_repository_owner_id}' && assertion.repository_id == '${var.github_repository_id}' && assertion.ref == '${var.deploy_ref}'"
}

moved {
  from = google_iam_workload_identity_pool_provider.github[0]
  to   = google_iam_workload_identity_pool_provider.github
}

# Allow workflows from the permitted repo+ref to impersonate the deploy SA.
resource "google_service_account_iam_member" "github_actions" {
  service_account_id = google_service_account.cudly_deploy.name
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/${google_iam_workload_identity_pool.github.name}/attribute.repository_id/${var.github_repository_id}"
}

moved {
  from = google_service_account_iam_member.github_actions[0]
  to   = google_service_account_iam_member.github_actions
}
