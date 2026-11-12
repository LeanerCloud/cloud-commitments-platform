# Issue #70: the ladder notice and settle failure alarm is off by default; with
# the flag on it must exist once, watch its own filter's metric in the per-stack
# namespace and notify the alerts topic.
mock_provider "aws" {
  mock_resource "aws_sns_topic" {
    defaults = {
      arn = "arn:aws:sns:us-east-1:111111111111:cudly-test-abcd1234-alerts"
    }
  }
}

variables {
  stack_name                  = "cudly-test-abcd1234"
  environment                 = "dev"
  aws_region                  = "us-east-1"
  compute_platform            = "lambda"
  function_name               = "cudly-test-api"
  db_instance_identifier      = "cudly-test-db"
  log_group_name              = "/aws/lambda/cudly-test-api-0001"
  enable_ladder_failure_alarm = true
}

run "ladder_alarm_with_flag_on" {
  command = apply

  assert {
    condition     = length(aws_cloudwatch_log_metric_filter.ladder_failure) == 1 && length(aws_cloudwatch_metric_alarm.ladder_failure) == 1
    error_message = "The ladder filter and alarm must each exist once when enable_ladder_failure_alarm is true."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.ladder_failure[0].metric_name == aws_cloudwatch_log_metric_filter.ladder_failure[0].metric_transformation[0].name
    error_message = "The ladder alarm must watch the metric its filter emits."
  }

  assert {
    condition     = aws_cloudwatch_metric_alarm.ladder_failure[0].alarm_actions == toset([aws_sns_topic.alerts.arn])
    error_message = "The ladder alarm must notify the alerts SNS topic."
  }

  assert {
    condition = (
      aws_cloudwatch_log_metric_filter.ladder_failure[0].metric_transformation[0].namespace == "CUDly/${var.stack_name}" &&
      aws_cloudwatch_metric_alarm.ladder_failure[0].namespace == "CUDly/${var.stack_name}"
    )
    error_message = "The ladder metric must be namespaced per stack."
  }
}
