package iacfiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCFNCrossAccountPrimaryRoleNameHasNoStaleDefault pins #1585: Terraform
// installs generate the Lambda role name, so the template must not default to
// the retired CloudFormation stack's CUDly-LambdaRole and must still reject
// ARNs and paths.
func TestCFNCrossAccountPrimaryRoleNameHasNoStaleDefault(t *testing.T) {
	p := cfnParameter(t, "cloudformation/stacks/CUDly-CrossAccount/template.yaml", "PrimaryRoleName")
	assert.Empty(t, p.Default, "a default would silently re-point the trust policy on updates")
	assert.True(t, cfnAccepts(t, p, "cudly-prod-1a2b3c4d-lambda-20260101000000000000000001"))
	assert.False(t, cfnAccepts(t, p, ""))
	assert.False(t, cfnAccepts(t, p, "lambda/CUDly-LambdaRole"))
	assert.False(t, cfnAccepts(t, p, "arn:aws:iam::123456789012"))
	assert.False(t, cfnAccepts(t, p, "arn:aws:iam::123456789012:role/x"))
}
