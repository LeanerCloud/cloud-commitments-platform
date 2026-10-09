locals {
  # Every OIDC `sub` this repo presents starts with this prefix once
  # scripts/bootstrap-github-deploy-config.sh has set the repo's sub claim
  # template to ["repository_owner_id", "repository_id", "context"]. Keep the
  # two in lockstep: the template decides the subject GitHub mints, this
  # decides the subject AWS accepts.
  github_oidc_sub_prefix = "repository_owner_id:${var.github_repository_owner_id}:repository_id:${var.github_repository_id}"

  # The subjects the deploy role trusts (see the rationale in role.tf).
  github_oidc_id_subs = [
    "${local.github_oidc_sub_prefix}:ref:refs/heads/main",

    # Bare deployment environments. Bound directly by
    # deploy-aws-lambda.yml's build-and-deploy / test-deployment jobs
    # (environment: ${{ needs.prepare.outputs.environment }}, always
    # dev/staging/prod per that job's own resolution logic), and --
    # once #1674 merges -- by cleanup-staging.yml's two AWS destroy
    # jobs (environment: staging) and destroy-fargate-dev.yml's
    # destroy job (environment: dev).
    "${local.github_oidc_sub_prefix}:environment:dev",
    "${local.github_oidc_sub_prefix}:environment:staging",
    "${local.github_oidc_sub_prefix}:environment:prod",

    # deploy-aws-fargate.yml's `deploy` job binds to
    # `aws-fargate-${{ needs.prepare.outputs.environment }}`, and that
    # output is always dev/staging/prod (workflow_dispatch choice
    # input, workflow_call from deploy-all.yml which is itself
    # choice-constrained, or the untriggered-input "dev" fallback).
    "${local.github_oidc_sub_prefix}:environment:aws-fargate-dev",
    "${local.github_oidc_sub_prefix}:environment:aws-fargate-staging",
    "${local.github_oidc_sub_prefix}:environment:aws-fargate-prod",

    # database-migration.yml's `migrate-aws` job binds to
    # `aws-db-${{ inputs.environment }}` on its `workflow_dispatch`
    # trigger, where `inputs.environment` is a choice input
    # constrained to dev/staging/prod. See the workflow_call caveat
    # above for what is deliberately excluded.
    "${local.github_oidc_sub_prefix}:environment:aws-db-dev",
    "${local.github_oidc_sub_prefix}:environment:aws-db-staging",
    "${local.github_oidc_sub_prefix}:environment:aws-db-prod",

    # rollback.yml's rollback-aws-lambda binds to plain
    # dev/staging/prod (covered above) and rollback-aws-fargate
    # binds to aws-fargate-<env> (covered above), matching each
    # job's own deploy workflow, rather than a compound
    # `<cloud>-<env>-rollback` name. See #139. This list previously
    # had six `aws-{lambda,fargate}-<env>-rollback` entries added
    # for #1648; nothing presents those subjects any more, so they
    # were removed rather than left as unused trust surface.
  ]

  # Transitional: while var.legacy_github_repo is set, the same subjects are
  # also trusted in the old name-keyed shape (`repo:<owner>/<name>:...`), so
  # deploys keep working until the repo's sub claim template is switched. Clear
  # the variable and re-apply after the cutover
  # (docs/deploy-trust-bootstrap.md).
  github_oidc_legacy_subs = var.legacy_github_repo == "" ? [] : [
    for s in local.github_oidc_id_subs :
    replace(s, local.github_oidc_sub_prefix, "repo:${var.legacy_github_repo}")
  ]

  github_oidc_subs = concat(local.github_oidc_id_subs, local.github_oidc_legacy_subs)
}

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]

  # SHA1 thumbprint of GitHub's OIDC TLS certificate.
  # AWS now validates GitHub tokens against the JWKS endpoint directly, so this
  # is effectively a formality, but at least one thumbprint is required by the API.
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]
}

moved {
  from = aws_iam_openid_connect_provider.github[0]
  to   = aws_iam_openid_connect_provider.github
}
