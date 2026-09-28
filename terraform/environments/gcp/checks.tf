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
    var.compute_platform == "cloud-run"
    ? module.compute_cloud_run[0].service_url
    : module.compute_gke[0].api_url,
    "/"
  )

  provider_name = "gcp"
}

# ==============================================
# Configuration Guards (#128)
# ==============================================
#
# `check` blocks, not `precondition`s: they run on every plan/apply and
# surface as a WARNING, but never fail the plan or block an apply. A
# blocking precondition would also block every future prod deploy (any
# unrelated change) until the LB/CDN/Cloud Armor stack is provisioned --
# a coordinated runtime cutover (DNS + certificate + a human-run apply,
# tracked separately) that this project's automated changes never perform
# blind. A non-blocking warning that cannot be missed on every plan is the
# guard that ships without waiting for that coordination.
#
# Today the first assert fails (WARN) for prod: environment = "prod" has
# enable_cdn = false, so the API is internet-reachable with no network-layer
# gate. That is the current, tracked state (#128), not a regression
# introduced here -- the point of this check is to make sure it cannot
# become "current" again silently once the LB/CDN cutover lands and someone
# edits a tfvars file back. The second assert is clean today: prod and
# staging's enable_cloud_armor was flipped to false alongside this change,
# since it was previously a no-op that misrepresented what protects the
# request path (#128) -- it guards against that combination reappearing.

check "prod_requires_network_authenticated_ingress" {
  assert {
    condition     = !(var.environment == "prod" && !var.enable_cdn)
    error_message = <<-EOT
      environment = "prod" with enable_cdn = false leaves Cloud Run
      allow_unauthenticated = true (see compute.tf) and cloud_run_ingress
      typically INGRESS_TRAFFIC_ALL: the API is reachable from the open
      internet with no network-layer gate, IAM-invoker gate, or Cloud Armor
      in the request path. This must be resolved by the enable_cdn = true
      cutover (LB + CloudFront-equivalent OAC + Cloud Armor), not by
      silencing this warning.
    EOT
  }
}

check "cloud_armor_must_sit_in_the_request_path" {
  assert {
    condition     = !(var.enable_cloud_armor && !var.enable_cdn)
    error_message = <<-EOT
      enable_cloud_armor = true with enable_cdn = false: the Cloud Armor
      policy is provisioned into nothing, since module.frontend (the only
      place that attaches it) only exists when enable_cdn = true (see
      frontend.tf). An operator reading this environment's tfvars is misled
      into believing Cloud Armor protects it. Either set enable_cloud_armor
      = false until the enable_cdn cutover lands, or complete that cutover.
    EOT
  }
}

# Guards against a partial cutover: enable_cdn = true provisions the LB +
# Cloud Armor, but neither of the two checks above notices if
# cloud_run_ingress is left at INGRESS_TRAFFIC_ALL (the *.run.app URL still
# answers direct internet traffic, bypassing the LB and Cloud Armor
# entirely). var.cloud_run_ingress's own default/validation already steers
# toward INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER; this check makes a drift
# from that pairing visible on every plan instead of only in the variable
# description (CodeRabbit finding on PR #401).
check "cdn_requires_restricted_ingress" {
  assert {
    condition     = !(var.enable_cdn && var.cloud_run_ingress == "INGRESS_TRAFFIC_ALL")
    error_message = <<-EOT
      enable_cdn = true with cloud_run_ingress = "INGRESS_TRAFFIC_ALL": the
      LB and Cloud Armor are provisioned, but the *.run.app URL still
      answers direct internet traffic, bypassing both. Set cloud_run_ingress
      = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" (the variable's default) so
      only requests through the LB reach the service.
    EOT
  }
}

# The inverse of cloud_armor_must_sit_in_the_request_path: warns if a future
# prod cutover enables the LB (enable_cdn = true) without also turning on
# Cloud Armor. Scoped to environment = "prod" (not every enable_cdn = true
# environment) since dev/staging may deliberately run the LB without a WAF.
# Passes today for every environment: none currently sets enable_cdn = true
# (CodeRabbit finding on PR #401).
check "prod_cdn_requires_cloud_armor" {
  assert {
    condition     = !(var.environment == "prod" && var.enable_cdn && !var.enable_cloud_armor)
    error_message = <<-EOT
      environment = "prod" with enable_cdn = true and enable_cloud_armor =
      false: the CDN request path has no Cloud Armor policy in front of it.
      Enable Cloud Armor as part of the production enable_cdn cutover.
    EOT
  }
}
