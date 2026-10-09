data "aws_caller_identity" "current" {}

resource "aws_iam_role" "cudly_deploy" {
  name = "cudly-terraform-deploy"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = concat(
      # Optional: allow a human IAM principal for local deployments
      var.trust_principal != "" ? [{
        Effect    = "Allow"
        Principal = { AWS = "arn:aws:iam::${data.aws_caller_identity.current.account_id}:${var.trust_principal}" }
        Action    = "sts:AssumeRole"
      }] : [],
      # GitHub Actions via OIDC
      [{
        Effect = "Allow"
        Principal = {
          Federated = aws_iam_openid_connect_provider.github.arn
        }
        Action = "sts:AssumeRoleWithWebIdentity"
        Condition = {
          StringEquals = {
            "token.actions.githubusercontent.com:aud" = "sts.amazonaws.com"
            # Restrict to main and named deployment environments. An any-ref
            # wildcard (`<prefix>:*`) would let a developer push to an
            # unprotected feature branch and mint valid deploy credentials.
            # Subjects carry the repository's immutable owner and repo IDs,
            # not its name, so a rename or a new repo reusing a freed name
            # neither gains nor loses deploy rights (see local
            # github_oidc_sub_prefix in github_oidc.tf).
            #
            # An OIDC `sub` is EITHER `...:ref:refs/heads/<branch>` OR
            # `...:environment:<name>` -- never both -- so every job in every
            # AWS-assuming workflow that binds to an `environment:` needs its
            # exact subject listed here, or it cannot authenticate at all. This
            # list is derived from grepping every `role-to-assume: ${{
            # vars.AWS_ROLE_TO_ASSUME }}` step across .github/workflows/ and
            # tracing each job's `environment:` value to its source (see #1648
            # and the PR that added this comment for the full per-job table).
            # `StringEquals` (exact match, not `StringLike`) on purpose: every
            # value below is enumerable from a `type: choice` input, so a
            # pattern would only widen what this policy admits, never narrow
            # it usefully.
            #
            # THIS LIST DOES NOT BY ITSELF RESTRICT DEPLOYS TO `main`. Owner
            # constraint is "only main may deploy". Three DIFFERENT controls
            # keep getting conflated in this repo's issues; this list is only
            # the first of them:
            #   1. Allowlist membership (THIS list): can the job authenticate
            #      to AWS at all? Nothing else below matters if this says no.
            #   2. Deployment branch policy (GitHub environment setting, NOT
            #      configured by this module; set to main-only by
            #      scripts/bootstrap-github-deploy-config.sh): which BRANCH
            #      may trigger a deploy to that environment. This is what
            #      "only main may deploy" actually requires.
            #   3. Required reviewers / protection rules (GitHub environment
            #      setting, manual, NOT configured by this module): WHO must
            #      approve before a job bound to that environment proceeds.
            #      The subject of #1591/#1674/#1660, not this file.
            #
            # `ref:refs/heads/main` enforces (2) on its own, for an unbound
            # job: no environment, no policy needed, the ref check IS the
            # restriction. `environment:<name>` subjects enforce NEITHER (2)
            # nor (3) on their own: they are ref-agnostic (the subject encodes
            # which environment the job bound to, not which branch triggered
            # the run), so a `workflow_dispatch` from ANY branch against an
            # environment-bound job presents the identical subject a `main`
            # run would. Until a deployment branch policy exists on that
            # environment, this allowlist delivers "only these environments
            # deploy, from any branch", not "only main deploys". Apply this module
            # first (with legacy_github_repo set), then run the bootstrap
            # script, which creates every environment below with a main-only
            # branch policy and switches the sub template; the ID-keyed
            # subjects are only presented after that switch. See
            # docs/deploy-trust-bootstrap.md.
            #
            # Deliberately NOT listed, and why:
            #   - `pull_request` subjects: no job that assumes THIS role
            #     (cudly_deploy) runs on `pull_request` in this repo. The
            #     read-only AWS sanity-check workflow that calls
            #     configure-aws-credentials on `pull_request` and assumes a
            #     separate, already-read-only role lives in
            #     cloud-commitments-go, not here. Also note GitHub does not
            #     mint OIDC tokens for `pull_request` runs from forked repos
            #     by default, independent of this policy.
            #   - `environment:aws-db-<anything>` from database-migration.yml's
            #     `workflow_call` trigger: that trigger declares `environment`
            #     as an unconstrained `type: string`, but nothing in this repo
            #     currently calls this workflow via `workflow_call` (grepped
            #     `.github/workflows/` for `uses:.*database-migration.yml`: no
            #     hits). Only the `workflow_dispatch` path, constrained to
            #     dev/staging/prod, is reachable today -- covered below. If a
            #     caller is ever added, this must be revisited before that
            #     caller can authenticate.
            "token.actions.githubusercontent.com:sub" = local.github_oidc_subs
          }
        }
      }]
    )
  })

  tags = {
    Project   = "CUDly"
    ManagedBy = "terraform"
  }
}

resource "aws_iam_role_policy_attachment" "networking" {
  role       = aws_iam_role.cudly_deploy.name
  policy_arn = aws_iam_policy.networking.arn
}

resource "aws_iam_role_policy_attachment" "compute" {
  role       = aws_iam_role.cudly_deploy.name
  policy_arn = aws_iam_policy.compute.arn
}

resource "aws_iam_role_policy_attachment" "compute_b" {
  role       = aws_iam_role.cudly_deploy.name
  policy_arn = aws_iam_policy.compute_b.arn
}

resource "aws_iam_role_policy_attachment" "data" {
  role       = aws_iam_role.cudly_deploy.name
  policy_arn = aws_iam_policy.data.arn
}

resource "aws_iam_role_policy_attachment" "iam" {
  role       = aws_iam_role.cudly_deploy.name
  policy_arn = aws_iam_policy.iam.arn
}
