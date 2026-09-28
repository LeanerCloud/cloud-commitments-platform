# ==============================================
# Post-deploy Validation Checks
# ==============================================
#
# Runs 13 check blocks during plan/apply (health, headers, public endpoints,
# auth enforcement, errors, methods, content-type, size limits, frontend)
# plus a local-exec provisioner for tests that need TLS introspection or
# multi-step auth flows. See modules/deployment-checks/ for details.

module "deployment_checks" {
  source = "../../modules/deployment-checks"

  api_base_url = trimsuffix(
    var.compute_platform == "lambda"
    ? one(module.compute_lambda[*].function_url)
    : one(module.compute_fargate[*].api_url),
    "/"
  )

  provider_name = "aws"
}

# ==============================================
# Configuration Guards (#128)
# ==============================================
#
# `check` block, not a `precondition`: it runs on every plan/apply and
# surfaces as a WARNING, but never fails the plan or blocks an apply. A
# blocking precondition would also block every future prod deploy (any
# unrelated change) until the CloudFront OAC cutover (#16) lands -- a
# coordinated runtime cutover (DNS + certificate + a human-run apply) this
# change does not perform. Mirrors
# terraform/environments/gcp/checks.tf's identical guard for the GCP half
# of the same defect.
#
# Today this assert fails (WARN) for prod: environment = "prod" has
# enable_cdn = false, so lambda_function_url_auth_type = "NONE" (compute.tf)
# leaves the Lambda Function URL reachable from the open internet with no
# IAM gate. That is the current, tracked state (#128, LeanerCloud/cloud-commitments-cli#575),
# not a regression introduced here -- the point of this check is to make
# sure it cannot become "current" again silently once the #16 cutover lands
# and someone edits a tfvars file back.

check "prod_requires_network_authenticated_ingress" {
  assert {
    condition     = !(var.environment == "prod" && !var.enable_cdn)
    error_message = <<-EOT
      environment = "prod" with enable_cdn = false leaves
      lambda_function_url_auth_type = "NONE" (see compute.tf): the Lambda
      Function URL is reachable from the open internet with no IAM gate.
      This must be resolved by the enable_cdn = true cutover (CloudFront +
      OAC, tracked by #16), not by silencing this warning.
    EOT
  }
}
