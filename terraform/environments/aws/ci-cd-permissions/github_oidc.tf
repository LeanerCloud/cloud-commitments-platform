locals {
  # Every OIDC `sub` this repo presents starts with this prefix once
  # scripts/bootstrap-github-deploy-config.sh has set the repo's sub claim
  # template to ["repository_owner_id", "repository_id", "context"]. Keep the
  # two in lockstep: the template decides the subject GitHub mints, this
  # decides the subject AWS accepts.
  github_oidc_sub_prefix = "repository_owner_id:${var.github_repository_owner_id}:repository_id:${var.github_repository_id}"
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
