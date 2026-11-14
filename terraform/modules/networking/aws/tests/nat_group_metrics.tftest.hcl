# fck-nat ASG group metric for the NAT outage alarm (#1763). The alarm is in
# environments/aws; this pins the module half, whose metric list the alarm
# asserts against.
#
# Mutation probes (temporary edits, each must fail a run below): drop the
# enabled_metrics expression; invert the variable test; metric name change;
# ignore enable_nat_gateway in the output.
mock_provider "aws" {
  # The AZ list and the VPC's IPv6 block feed cidrsubnet() and indexing, so
  # the mock's random defaults cannot stand in for them.
  override_data {
    target = data.aws_availability_zones.available
    values = {
      names = ["us-east-1a", "us-east-1b", "us-east-1c"]
    }
  }

  override_resource {
    target = aws_vpc.main
    values = {
      ipv6_cidr_block = "2600:1f18:1234:5600::/56"
    }
  }

  override_data {
    target = data.aws_vpc.existing[0]
    values = {
      cidr_block      = "10.0.0.0/16"
      ipv6_cidr_block = "2600:1f18:1234:5600::/56"
    }
  }

  # The provider validates the ASG's launch_template.id format.
  override_resource {
    target = aws_launch_template.fck_nat
    values = {
      id = "lt-0mock"
    }
  }
}

variables {
  stack_name               = "cudly-test"
  permissions_boundary_arn = "arn:aws:iam::123456789012:policy/cudly-deploy-boundary"
  region                   = "us-east-1"
  az_count                 = 3
  enable_nat_gateway       = true
}

run "default_publishes_no_group_metrics" {
  command = apply

  assert {
    condition     = length(aws_autoscaling_group.fck_nat[0].enabled_metrics) == 0
    error_message = "enabled_metrics must stay empty (the provider default) unless asked for, so current stacks see no diff."
  }

  assert {
    condition     = length(output.nat_autoscaling_group_enabled_metrics) == 0
    error_message = "The output must mirror the ASG's enabled_metrics."
  }
}

run "group_metrics_publish_in_service_count" {
  command = apply

  variables {
    enable_nat_group_metrics = true
  }

  assert {
    condition     = toset(aws_autoscaling_group.fck_nat[0].enabled_metrics) == toset(["GroupInServiceInstances"])
    error_message = "The NAT outage alarm reads GroupInServiceInstances, so the ASG must publish exactly that."
  }

  assert {
    condition     = toset(output.nat_autoscaling_group_enabled_metrics) == toset(["GroupInServiceInstances"])
    error_message = "The output must expose the published metrics for the environment alarm's precondition."
  }

  # min = max = 1 is what makes "< 1 in service" mean "no NAT".
  assert {
    condition     = aws_autoscaling_group.fck_nat[0].min_size == 1 && aws_autoscaling_group.fck_nat[0].max_size == 1
    error_message = "The alarm threshold assumes the NAT ASG is pinned to exactly one instance."
  }
}

run "no_nat_gateway_no_metrics_output" {
  command = plan

  variables {
    enable_nat_group_metrics = true
    enable_nat_gateway       = false
    # Module-created private subnets require the NAT, so use an existing VPC.
    use_existing_vpc            = true
    existing_vpc_id             = "vpc-0123456789abcdef0"
    existing_public_subnet_ids  = ["subnet-0a", "subnet-0b"]
    existing_private_subnet_ids = ["subnet-0c", "subnet-0d"]
  }

  assert {
    condition     = output.nat_autoscaling_group_enabled_metrics == null
    error_message = "Without the NAT there is no ASG, so the output must be null."
  }
}
