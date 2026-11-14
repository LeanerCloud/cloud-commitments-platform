# NAT outage alarm wiring (#1763). The alarm is in monitoring.tf; these runs
# pin its shape, its gate and its SNS wiring. Same offline setup as
# fargate_cdn_alb.tftest.hcl.
#
# Mutation probes (temporary edits, each must fail a run below): threshold
# 1 -> 2; datapoints_to_alarm 2 -> 1; count gate inverted; alarm_actions -> [];
# rename the dimension; metric_name -> GroupTerminatingInstances; networking
# flag no longer passed.
mock_provider "aws" {
  mock_data "aws_ec2_managed_prefix_list" {
    defaults = {
      id = "pl-3b927c52"
    }
  }

  mock_data "aws_availability_zones" {
    defaults = {
      names = ["us-east-1a", "us-east-1b", "us-east-1c"]
    }
  }

  # main.tf builds permissions_boundary_arn from this, and the provider
  # rejects an ARN whose account is not 12 digits. Synthetic placeholder.
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
    }
  }
}

mock_provider "random" {}
mock_provider "http" {}
mock_provider "external" {
  mock_data "external" {
    defaults = {
      result = { arch = "arm64" }
    }
  }
}

# Apply (not plan) so the ASG name and the alarm's dimension are known; these
# are the module tests' overrides (modules/networking/aws/tests).
override_resource {
  target = module.networking.aws_vpc.main
  values = {
    ipv6_cidr_block = "2600:1f18:1234:5600::/56"
  }
}

# The provider validates alarm_actions as ARNs.
override_resource {
  target = module.monitoring[0].aws_sns_topic.alerts
  values = {
    arn = "arn:aws:sns:us-east-1:123456789012:cudly-dev-alerts"
  }
}

override_resource {
  target = module.networking.aws_launch_template.fck_nat
  values = {
    id = "lt-0mock"
  }
}

override_module {
  target = module.build
}

override_module {
  target = module.registry
}

# outputs.tf interpolates these into a heredoc, which rejects nulls.
override_module {
  target = module.database
  outputs = {
    database_name        = "cudly"
    instance_address     = "db.example.com"
    proxy_endpoint       = "proxy.example.com"
    password_secret_name = "cudly-test-db-password"
    instance_identifier  = "cudly-test-db"
  }
}

override_module {
  target = module.secrets
}

override_module {
  target = module.compute_lambda
  outputs = {
    function_name  = "cudly-test-api"
    function_url   = "https://abc123.lambda-url.us-east-1.on.aws/"
    log_group_name = "/aws/lambda/cudly-test-api"
  }
}

override_module {
  target = module.compute_fargate
  outputs = {
    alb_dns_name   = "cudly-test-alb.us-east-1.elb.amazonaws.com"
    api_url        = "http://cudly-test-alb.us-east-1.elb.amazonaws.com"
    log_group_name = "/ecs/cudly-test"
  }
}

override_module {
  target = module.frontend
  outputs = {
    frontend_url = "https://app.example.com"
  }
}

override_module {
  target = module.deployment_checks
}


variables {
  environment         = "dev"
  region              = "us-east-1"
  compute_platform    = "fargate"
  enable_docker_build = false
  image_uri           = "123456789012.dkr.ecr.us-east-1.amazonaws.com/cudly:test"
  admin_email         = "admin@example.com"
  enable_flow_logs    = false
}

run "default_off_creates_nothing" {
  command = plan

  assert {
    condition     = length(aws_cloudwatch_metric_alarm.nat_churn) == 0
    error_message = "No alarm may exist unless enable_nat_churn_alarm is set."
  }

  assert {
    condition     = length(module.networking.nat_autoscaling_group_enabled_metrics) == 0
    error_message = "The ASG must not publish group metrics unless the alarm is on (no diff for current stacks)."
  }
}

run "enabled_alarm_shape" {
  command = apply

  variables {
    enable_nat_churn_alarm = true
  }

  # Monitoring is off here, which is exactly what the check warns about.
  expect_failures = [check.nat_churn_alarm_has_target]

  assert {
    condition = (
      length(aws_cloudwatch_metric_alarm.nat_churn) == 1
      && can(regex("^cudly-dev-[0-9a-z]+-nat-instance-churn$", aws_cloudwatch_metric_alarm.nat_churn[0].alarm_name))
    )
    error_message = "The alarm must be named <stack_name>-nat-instance-churn, inside the deploy role's alarm:cudly-* grant."
  }

  assert {
    condition = (
      aws_cloudwatch_metric_alarm.nat_churn[0].namespace == "AWS/AutoScaling"
      && aws_cloudwatch_metric_alarm.nat_churn[0].metric_name == "GroupInServiceInstances"
      && aws_cloudwatch_metric_alarm.nat_churn[0].statistic == "Minimum"
      && aws_cloudwatch_metric_alarm.nat_churn[0].comparison_operator == "LessThanThreshold"
      && aws_cloudwatch_metric_alarm.nat_churn[0].threshold == 1
      && aws_cloudwatch_metric_alarm.nat_churn[0].period == 300
      && aws_cloudwatch_metric_alarm.nat_churn[0].evaluation_periods == 3
      && aws_cloudwatch_metric_alarm.nat_churn[0].datapoints_to_alarm == 2
      && aws_cloudwatch_metric_alarm.nat_churn[0].treat_missing_data == "notBreaching"
    )
    error_message = "Alarm must be Minimum GroupInServiceInstances < 1 over 300s, 2 of 3 datapoints, missing data not breaching."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.nat_churn[0].dimensions["AutoScalingGroupName"] == module.networking.nat_autoscaling_group_name
    error_message = "The alarm must watch the fck-nat ASG by name."
  }

  assert {
    condition     = contains(module.networking.nat_autoscaling_group_enabled_metrics, aws_cloudwatch_metric_alarm.nat_churn[0].metric_name)
    error_message = "The ASG must publish the metric the alarm reads."
  }

  assert {
    condition     = length(aws_cloudwatch_metric_alarm.nat_churn[0].alarm_actions) == 0 && length(aws_cloudwatch_metric_alarm.nat_churn[0].ok_actions) == 0
    error_message = "Without monitoring there is no topic, so no actions."
  }
}

run "monitoring_topic_receives_alarm_and_ok" {
  command = apply

  variables {
    enable_nat_churn_alarm = true
    enable_monitoring      = true
  }

  assert {
    condition     = length(aws_cloudwatch_metric_alarm.nat_churn[0].alarm_actions) == 1 && length(aws_cloudwatch_metric_alarm.nat_churn[0].ok_actions) == 1
    error_message = "With monitoring on, the monitoring SNS topic must be notified on ALARM and OK."
  }

  assert {
    condition     = contains(aws_cloudwatch_metric_alarm.nat_churn[0].alarm_actions, "arn:aws:sns:us-east-1:123456789012:cudly-dev-alerts") && contains(aws_cloudwatch_metric_alarm.nat_churn[0].ok_actions, "arn:aws:sns:us-east-1:123456789012:cudly-dev-alerts")
    error_message = "Both action lists must carry the monitoring topic's ARN."
  }
}
