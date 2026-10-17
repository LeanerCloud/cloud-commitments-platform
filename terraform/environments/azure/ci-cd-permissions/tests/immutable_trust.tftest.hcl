mock_provider "azurerm" {}
mock_provider "azuread" {}

variables {
  subscription_id = "11111111-1111-1111-1111-111111111111"
  github_owner_id = "86753534"
  github_repo_id  = "1391259743"
}

run "subjects_use_ids" {
  command = plan
  assert {
    condition     = azuread_application_federated_identity_credential.github_main[0].subject == "repo:LeanerCloud@86753534/cloud-commitments-platform@1391259743:ref:refs/heads/main"
    error_message = "main credential must use the id-keyed subject."
  }
  assert {
    condition = (
      length(azuread_application_federated_identity_credential.github_environment) == length(var.github_environments) &&
      alltrue([for e in var.github_environments :
        azuread_application_federated_identity_credential.github_environment[e].subject == "repo:LeanerCloud@86753534/cloud-commitments-platform@1391259743:environment:${e}"
      ])
    )
    error_message = "every environment credential must use the id-keyed subject."
  }
  assert {
    condition = (
      azuread_application_federated_identity_credential.github_main[0].subject != "repo:LeanerCloud/cloud-commitments-platform:ref:refs/heads/main" &&
      alltrue([for c in azuread_application_federated_identity_credential.github_environment : !startswith(c.subject, "repo:LeanerCloud/")])
    )
    error_message = "No name-only subject may remain."
  }
}

run "custom_environments_all_keyed" {
  command = plan
  variables {
    github_environments = ["dev", "qa"]
  }
  assert {
    condition = (
      length(azuread_application_federated_identity_credential.github_environment) == 2 &&
      azuread_application_federated_identity_credential.github_environment["qa"].subject == "repo:LeanerCloud@86753534/cloud-commitments-platform@1391259743:environment:qa"
    )
    error_message = "environment credentials must follow github_environments."
  }
}

run "skip_mode_creates_no_trust" {
  command = plan
  variables {
    github_repo = ""
  }
  assert {
    condition     = length(azuread_application_federated_identity_credential.github_main) == 0 && length(azuread_application_federated_identity_credential.github_environment) == 0
    error_message = "github_repo = \"\" must create no federated credentials."
  }
}

run "owner_id_rejects_empty" {
  command = plan
  variables {
    github_owner_id = ""
  }
  expect_failures = [var.github_owner_id]
}

run "owner_id_rejects_text" {
  command = plan
  variables {
    github_owner_id = "abc"
  }
  expect_failures = [var.github_owner_id]
}

run "repo_id_rejects_empty" {
  command = plan
  variables {
    github_repo_id = ""
  }
  expect_failures = [var.github_repo_id]
}

run "repo_id_rejects_leading_zero" {
  command = plan
  variables {
    github_repo_id = "0123"
  }
  expect_failures = [var.github_repo_id]
}

run "repo_rejects_quote" {
  command = plan
  variables {
    github_repo = "a/b'c"
  }
  expect_failures = [var.github_repo]
}

run "repo_rejects_no_slash" {
  command = plan
  variables {
    github_repo = "noslash"
  }
  expect_failures = [var.github_repo]
}
