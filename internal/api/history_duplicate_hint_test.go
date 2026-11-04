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
}
