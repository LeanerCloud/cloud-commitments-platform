mock_provider "aws" {
  mock_resource "aws_iam_policy" {
    defaults = { arn = "arn:aws:iam::123456789012:policy/mock" }
  }
  mock_resource "aws_iam_openid_connect_provider" {
    defaults = { arn = "arn:aws:iam::123456789012:oidc-provider/token.actions.githubusercontent.com" }
  }
  mock_resource "aws_iam_role" {
    defaults = { arn = "arn:aws:iam::123456789012:role/mock" }
  }
  mock_data "aws_caller_identity" {
    defaults = { arn = "arn:aws:iam::123456789012:user/mock" }
  }
}

variables {
  github_owner_id = "86753534"
  github_repo_id  = "1391259743"
}

run "subjects_use_ids" {
  command = apply
  assert {
    condition = jsondecode(aws_iam_role.cudly_deploy.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"] == [
      for s in [
        "ref:refs/heads/main",
        "environment:dev", "environment:staging", "environment:prod",
        "environment:aws-fargate-dev", "environment:aws-fargate-staging", "environment:aws-fargate-prod",
        "environment:aws-db-dev", "environment:aws-db-staging", "environment:aws-db-prod",
      ] : "repo:LeanerCloud@86753534/cloud-commitments-platform@1391259743:${s}"
    ]
    error_message = "AWS trust subjects must be exactly the 10 id-keyed subjects."
  }
  assert {
    condition     = !contains(jsondecode(aws_iam_role.cudly_deploy.assume_role_policy).Statement[0].Condition.StringEquals["token.actions.githubusercontent.com:sub"], "repo:LeanerCloud/cloud-commitments-platform:ref:refs/heads/main")
    error_message = "No name-only subject may remain."
  }
}

run "skip_mode_creates_no_trust" {
  command = apply
  variables {
    github_repo = ""
  }
  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 0 && length(jsondecode(aws_iam_role.cudly_deploy.assume_role_policy).Statement) == 0
    error_message = "github_repo = \"\" must create no OIDC provider and no web-identity statement."
  }
}

run "owner_id_rejects_bad" {
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
