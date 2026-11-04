package cicdpermissions_test

import (
	"strings"
	"testing"
)

// cdnApplyActions are the IAM actions an enable_cdn = true Lambda apply needs
// (modules/frontend/aws, the OAC and CloudFront function, and the Lambda
// Function URL flip to AWS_IAM). A missing one fails the apply half-way, after
// the Function URL is already closed, and leaves the environment returning 403
// (#16, found twice in review). createTime actions run while the resource does
// not exist yet, so an aws:ResourceTag-only grant can never authorize them.
var cdnApplyActions = []struct {
	action     string
	createTime bool
}{
	{"cloudfront:CreateDistribution", false},
	{"cloudfront:CreateDistributionWithTags", true},
	{"cloudfront:TagResource", true},
	{"cloudfront:GetDistribution", false},
	{"cloudfront:GetDistributionConfig", false},
	{"cloudfront:UpdateDistribution", false},
	{"cloudfront:DeleteDistribution", false},
	{"cloudfront:ListTagsForResource", false},
	{"cloudfront:CreateOriginAccessControl", false},
	{"cloudfront:GetOriginAccessControl", false},
	{"cloudfront:UpdateOriginAccessControl", false},
	{"cloudfront:DeleteOriginAccessControl", false},
	{"cloudfront:ListOriginAccessControls", false},
	{"cloudfront:CreateFunction", false},
	{"cloudfront:DescribeFunction", false},
	{"cloudfront:GetFunction", false},
	{"cloudfront:PublishFunction", false},
	{"cloudfront:UpdateFunction", false},
	{"cloudfront:DeleteFunction", false},
	{"lambda:AddPermission", false},
	{"lambda:RemovePermission", false},
	{"lambda:GetPolicy", false},
	{"lambda:CreateFunctionUrlConfig", false},
	{"lambda:GetFunctionUrlConfig", false},
	{"lambda:UpdateFunctionUrlConfig", false},
	{"lambda:DeleteFunctionUrlConfig", false},
}

func TestDeployPolicyGrantsEveryActionTheCDNApplyNeeds(t *testing.T) {
	var allow []string // statement blocks that Allow
	for _, f := range guardedPolicyFiles(t) {
		for _, stmt := range extractStatementBlocks(readPolicySource(t, f)) {
			if statementEffect(stmt) == "Allow" {
				allow = append(allow, stmt)
			}
		}
	}
	if len(allow) == 0 {
		t.Fatal("found no Allow statements; the statement splitter is broken and this test would pass vacuously")
	}

	for _, need := range cdnApplyActions {
		granted := false
		for _, stmt := range allow {
			if !actionPermitted(statementActions(stmt), need.action) {
				continue
			}
			cond := statementConditionBody(stmt)
			if need.createTime && cond != "" && !strings.Contains(cond, "aws:RequestTag/") {
				continue // gated on tags the new resource cannot have yet
			}
			granted = true
			break
		}
		if !granted {
			why := ""
			if need.createTime {
				why = " (it runs at create time, so it needs an unconditioned grant or one gated on aws:RequestTag, not aws:ResourceTag alone)"
			}
			t.Errorf("no deploy policy statement grants %s%s; an enable_cdn apply would fail after the Function URL flips to AWS_IAM", need.action, why)
		}
	}
}
