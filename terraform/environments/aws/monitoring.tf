# ==============================================
# Monitoring and failure alerts (issue #714)
# ==============================================
#
# Instantiates modules/monitoring/aws: an SNS alerts topic with one email
# subscription per address in var.alert_emails, CloudWatch alarms on the
# Lambda function and the RDS instance, and log-metric alarms on any failed
# purchase execution or failed scheduled task.
#
# Off by default, like modules/compute/aws/lambda's migration alarm: the SNS
# topic, dashboard, metric filters and query definitions need deploy-role
# grants (sns:*, cloudwatch:PutDashboard, logs:PutMetricFilter,
# logs:PutQueryDefinition) that only exist once the ci-cd-permissions
# bootstrap has been re-applied (root CLAUDE.md "CI/CD IAM" split). Turning
# this on first would 403 every apply. To enable:
#   1. re-apply terraform/environments/aws/ci-cd-permissions,
#   2. set the ALERT_EMAILS repository secret (comma-separated addresses),
#   3. set enable_monitoring = true in the environment's tfvars.
# Each address must confirm the SNS subscription email before alerts arrive.

locals {
  alert_email_addresses = compact([for e in split(",", var.alert_emails) : trimspace(e)])
}

module "monitoring" {
  source = "../../modules/monitoring/aws"
  count  = var.enable_monitoring ? 1 : 0

  stack_name       = local.stack_name
  environment      = var.environment
  aws_region       = var.region
  compute_platform = var.compute_platform

  function_name = var.compute_platform == "lambda" ? one(module.compute_lambda[*].function_name) : ""
  log_group_name = (
    var.compute_platform == "lambda"
    ? one(module.compute_lambda[*].log_group_name)
    : one(module.compute_fargate[*].log_group_name)
  )
  db_instance_identifier = module.database.instance_identifier

  alert_email_addresses       = local.alert_email_addresses
  enable_ladder_failure_alarm = var.enable_ladder_failure_alarm

  tags = local.common_tags
}

# Warns (never blocks) when prod would run with no alert recipient. Same
# check-not-precondition reasoning as checks.tf: this must not stop an
# unrelated prod deploy.
check "prod_alerts_reach_someone" {
  assert {
    condition     = !(var.environment == "prod" && (!var.enable_monitoring || length(local.alert_email_addresses) == 0))
    error_message = "environment = \"prod\" has no alert recipient: set enable_monitoring = true and the ALERT_EMAILS secret (issue #714)."
  }
}

# NAT outage / restart-loop alarm (#1763, #1090). A failed persistent-ENI
# attach runs `poweroff`, the instance terminates and the ASG replaces it,
# about every 5 minutes. The instance leaves by in-guest shutdown, so
# GroupTerminatingInstances (a once-a-minute gauge) mostly misses it;
# GroupInServiceInstances dips to 0 during each replacement. The NAT ASG is
# min = max = 1 (modules/networking/aws), so < 1 in service means no NAT.
# Fires when that holds in 2 of 3 consecutive 5-minute windows. Thresholds are
# reasoned from the loop timing, not observed.
#
# This lives here, not in modules/networking, because it targets the
# monitoring topic: passing it into networking is a module-graph cycle
# (monitoring -> compute/database -> networking), which terraform validate
# reports. The alarm has no dependents, so no cycle here.
#
# If SNS encryption is ever enabled on the topic, its KMS key needs a grant for
# cloudwatch.amazonaws.com or the alarm's notifications are dropped.
resource "aws_cloudwatch_metric_alarm" "nat_churn" {
  count = var.enable_nat_churn_alarm ? 1 : 0

  alarm_name          = "${local.stack_name}-nat-instance-churn"
  alarm_description   = "The fck-nat instance was missing from service in 2 of 3 consecutive 5-minute windows: a restart loop (persistent-ENI attach failing, #1090) or any NAT outage. Private IPv4 egress is down or flapping. See the ASG activity history and the fck-nat user data."
  namespace           = "AWS/AutoScaling"
  metric_name         = "GroupInServiceInstances"
  statistic           = "Minimum"
  period              = 300
  evaluation_periods  = 3
  datapoints_to_alarm = 2
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"

  dimensions = {
    AutoScalingGroupName = module.networking.nat_autoscaling_group_name
  }

  # Length is known at plan time even though the topic ARN is not.
  alarm_actions = var.enable_monitoring ? [one(module.monitoring[*].sns_topic_arn)] : []
  ok_actions    = var.enable_monitoring ? [one(module.monitoring[*].sns_topic_arn)] : []

  tags = local.common_tags

  lifecycle {
    # Keeps the alarm and the ASG's enabled_metrics from drifting apart.
    precondition {
      condition     = contains(module.networking.nat_autoscaling_group_enabled_metrics, "GroupInServiceInstances")
      error_message = "The NAT ASG must publish GroupInServiceInstances, which this alarm reads."
    }
  }
}

# Warns (never blocks) when the NAT outage alarm is on with nowhere to send it:
# the alarm still exists but only shows in the console.
check "nat_churn_alarm_has_target" {
  assert {
    condition     = !var.enable_nat_churn_alarm || var.enable_monitoring
    error_message = "enable_nat_churn_alarm = true without enable_monitoring: the alarm will notify no one (issue #1763)."
  }
}
