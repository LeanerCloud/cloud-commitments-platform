package api

import (
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestHistoryRow_DuplicateGuardHint(t *testing.T) {
	ec2 := []config.RecommendationRecord{{Provider: "aws", Service: "ec2"}}
	denied := executionToHistoryRow(config.PurchaseExecution{Status: "failed", Error: fakeAWSDenied, Recommendations: ec2}, "", "")
	assert.False(t, strings.Contains(denied.OpsHint, "duplicate guard"), "definite refusal hides the warning")
	unknown := executionToHistoryRow(config.PurchaseExecution{Status: "failed", Error: "purchase outcome unknown", Recommendations: ec2}, "", "")
	assert.Contains(t, unknown.OpsHint, "duplicate guard")
	assert.True(t, unknown.RedriveUnsafe)

	// One refused rec does not prove that another, whose outcome is unknown, bought nothing.
	mixed := executionToHistoryRow(config.PurchaseExecution{Status: "failed", Error: "t4g.nano: purchase outcome unknown; t4g.micro: " + fakeAWSDenied, Recommendations: ec2}, "", "")
	assert.Contains(t, mixed.OpsHint, "duplicate guard", "a mixed failure keeps the warning")
	allDenied := executionToHistoryRow(config.PurchaseExecution{Status: "failed", Error: "t4g.nano: " + fakeAWSDenied + "; t4g.micro: " + fakeAWSDenied, Recommendations: ec2}, "", "")
	assert.NotContains(t, allDenied.OpsHint, "duplicate guard", "every rec refused hides the warning")
}
