// Regression test for issue #126: aws_lambda_function.main had no
// logging_config, so Lambda wrote to its own auto-created
// /aws/lambda/<function_name> group (retention "Never expire") regardless of
// what aws_cloudwatch_log_group.lambda's name_prefix produced. That left
// var.log_retention_days applied to an empty, unreferenced group, and made
// the migration-failure metric filter (migration-alarm.tf) match nothing
// because it attached to the same empty group.
//
// Fix keeps name_prefix (an exact "name" would collide with the group Lambda
// already auto-created in every deployed environment -- ResourceAlreadyExistsException,
// with nothing to import into) and instead points Lambda at the
// Terraform-managed group explicitly via logging_config.
package aws_test

import (
	"os"
	"regexp"
	"testing"
)

// lambdaLogGroupBlockPattern isolates the aws_cloudwatch_log_group "lambda"
// resource block so assertions below can't accidentally match an unrelated
// resource elsewhere in the file.
var lambdaLogGroupBlockPattern = regexp.MustCompile(`(?s)resource\s+"aws_cloudwatch_log_group"\s+"lambda"\s*\{.*?\n\}`)

// lambdaFunctionBlockPattern isolates aws_lambda_function.main. Matches up to
// the first top-level "}\n\n" after the opening brace, which is safe here
// because the resource's nested blocks (environment, vpc_config,
// logging_config) never contain a blank line before their own closing brace.
var lambdaFunctionBlockPattern = regexp.MustCompile(`(?s)resource\s+"aws_lambda_function"\s+"main"\s*\{.*?\n\}\n`)

func TestLambdaLogGroupUsesExactName(t *testing.T) {
	data, err := os.ReadFile("lambda/main.tf")
	if err != nil {
		t.Fatalf("reading lambda/main.tf: %v", err)
	}
	content := string(data)

	logGroupBlock := lambdaLogGroupBlockPattern.FindString(content)
	if logGroupBlock == "" {
		t.Fatal(`lambda/main.tf has no resource "aws_cloudwatch_log_group" "lambda" block; the log group Lambda writes to must be Terraform-managed so retention_in_days and the migration-failure metric filter apply to it`)
	}

	if regexp.MustCompile(`\bname\s*=\s*"`).MatchString(logGroupBlock) {
		t.Error(`aws_cloudwatch_log_group.lambda sets an exact "name". That collides with the fixed /aws/lambda/<function_name> group Lambda already auto-created in every deployed environment before this module managed a log group (ResourceAlreadyExistsException, nothing to import into). Use name_prefix and point Lambda at it via logging_config instead.`)
	}

	if !regexp.MustCompile(`\bname_prefix\s*=\s*"/aws/lambda/\$\{var\.stack_name\}-api-"`).MatchString(logGroupBlock) {
		t.Error(`aws_cloudwatch_log_group.lambda must set name_prefix = "/aws/lambda/${var.stack_name}-api-", a literal not derived from aws_lambda_function.main (deriving it from the function would cycle against aws_lambda_function.main.logging_config referencing this group)`)
	}

	functionBlock := lambdaFunctionBlockPattern.FindString(content)
	if functionBlock == "" {
		t.Fatal(`lambda/main.tf has no resource "aws_lambda_function" "main" block`)
	}

	if !regexp.MustCompile(`logging_config\s*\{[^}]*log_group\s*=\s*aws_cloudwatch_log_group\.lambda\.name\b`).MatchString(functionBlock) {
		t.Error(`aws_lambda_function.main must have a logging_config block with log_group = aws_cloudwatch_log_group.lambda.name (issue #126), or Lambda writes to its own auto-created default group regardless of what aws_cloudwatch_log_group.lambda's name_prefix produces`)
	}

	// The metric filter must attach to that same Terraform-managed group, not
	// re-derive its own name (which would silently drift from the check above).
	alarmData, err := os.ReadFile("lambda/migration-alarm.tf")
	if err != nil {
		t.Fatalf("reading lambda/migration-alarm.tf: %v", err)
	}
	if !regexp.MustCompile(`log_group_name\s*=\s*aws_cloudwatch_log_group\.lambda\.name\b`).MatchString(string(alarmData)) {
		t.Error(`aws_cloudwatch_log_metric_filter.migration_failed must set log_group_name = aws_cloudwatch_log_group.lambda.name, so the migration-failure alarm (issue #126) attaches to the exact group Lambda writes to`)
	}
}
