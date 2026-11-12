# ==============================================
# Ladder notice and settle failure alarm (issue #70)
# ==============================================
#
# The ladder fire task (internal/purchase/ladder_fire_notice.go) only logs its
# failures at ERROR; there is no metric. This filter matches those log lines
# and the alarm notifies the alerts SNS topic on any occurrence:
#
#   notice failed:                      the tranche notice could not be sent
#   could not be settled after the      the failed notice could not be settled,
#     failed notice                     so the tranche stays fired and unannounced
#   was claimed but its execution is    a claimed tranche has no execution row
#     unknown
#
# Off by default (var.enable_ladder_failure_alarm) until the ladder is
# scheduled. The match strings are the Go log text; keep them in step with
# ladder_fire_notice.go.

locals {
  ladder_failure_pattern = "?\"notice failed:\" ?\"could not be settled after the failed notice\" ?\"was claimed but its execution is unknown\""
}

resource "aws_cloudwatch_log_metric_filter" "ladder_failure" {
  count = var.enable_ladder_failure_alarm ? 1 : 0

  name           = "${var.stack_name}-ladder-failure"
  log_group_name = var.log_group_name
  pattern        = local.ladder_failure_pattern

  metric_transformation {
    name          = "LadderNoticeOrSettleFailed"
    namespace     = "CUDly/${var.stack_name}"
    value         = "1"
    default_value = "0"
  }
}

resource "aws_cloudwatch_metric_alarm" "ladder_failure" {
  count = var.enable_ladder_failure_alarm ? 1 : 0

  alarm_name          = "${var.stack_name}-ladder-failure"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = aws_cloudwatch_log_metric_filter.ladder_failure[0].metric_transformation[0].name
  namespace           = aws_cloudwatch_log_metric_filter.ladder_failure[0].metric_transformation[0].namespace
  period              = 300
  statistic           = "Sum"
  threshold           = 0
  alarm_description   = "A ladder tranche notice failed to send, or the failed notice could not be settled. Search the log group for ladder_fire: and the tranche and execution ids, then check History."
  alarm_actions       = [aws_sns_topic.alerts.arn]
  ok_actions          = [aws_sns_topic.alerts.arn]
  treat_missing_data  = "notBreaching"

  tags = var.tags
}
