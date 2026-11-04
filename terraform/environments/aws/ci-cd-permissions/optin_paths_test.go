package cicdpermissions_test

import "testing"

// TestDeployPolicyGrantsTagAndRetentionRemoval covers calls the AWS provider
// makes when an attribute is REMOVED rather than set, which a first apply never
// exercises (#157). Real ARN shapes per the Service Authorization Reference.
func TestDeployPolicyGrantsTagAndRetentionRemoval(t *testing.T) {
	const logs = "arn:aws:logs:us-east-1:123456789012:log-group:"
	assertGranted(t, "acm/logs removal", []grantNeed{
		// aws_acm_certificate.frontend drops a tag key (environments/aws/acm.tf).
		{"acm:RemoveTagsFromCertificate", "arn:aws:acm:us-east-1:123456789012:certificate/11111111-2222-3333-4444-555555555555"},
		// A log group retention going from N days to 0 (never expire).
		{"logs:DeleteRetentionPolicy", logs + "/aws/lambda/cudly-prod-abc-api-20260101"},
		{"logs:DeleteRetentionPolicy", logs + "/ecs/cudly-prod-abc"},
		{"logs:DeleteRetentionPolicy", logs + "/aws/vpc/cudly-prod-abc"},
	})
}

// TestPutRetentionPolicyIsPairedWithDelete: a statement that can set a log
// retention must also be able to clear it, or lowering it to 0 fails the apply.
func TestPutRetentionPolicyIsPairedWithDelete(t *testing.T) {
	seen := false
	for _, stmt := range deployAllowStatements(t) {
		if !actionPermitted(statementActions(stmt), "logs:PutRetentionPolicy") {
			continue
		}
		seen = true
		if !actionPermitted(statementActions(stmt), "logs:DeleteRetentionPolicy") {
			t.Errorf("statement %q grants logs:PutRetentionPolicy without logs:DeleteRetentionPolicy", statementSid(stmt))
		}
	}
	if !seen {
		t.Fatal("no statement grants logs:PutRetentionPolicy; the pairing check would pass vacuously")
	}
}

// TestDeployPolicyGrantsHostedZoneLifecycle covers create_subdomain_zone = true
// (environments/aws/route53.tf). CreateHostedZone has no resource type; the
// hostedzone ARN carries an opaque id and the SAR defines no condition keys for
// it, so neither a name scope nor a tag gate is possible (#157).
func TestDeployPolicyGrantsHostedZoneLifecycle(t *testing.T) {
	const zone = "arn:aws:route53:::hostedzone/Z0123456789ABCDEFGHIJ"
	assertGranted(t, "route53 zone", []grantNeed{
		{"route53:CreateHostedZone", ""},
		{"route53:ChangeTagsForResource", zone},
		{"route53:DeleteHostedZone", zone},
	})
}
