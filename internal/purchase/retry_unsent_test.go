package purchase

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

func retryExec(status string, ns bool, attempt int, recs ...config.RecommendationRecord) *config.PurchaseExecution {
	return &config.PurchaseExecution{Status: status, NoSubmission: ns, RetryAttemptN: attempt, Recommendations: recs}
}

func TestRetryRefusalReason(t *testing.T) {
	ec2 := config.RecommendationRecord{Provider: "aws", Service: "ec2"}
	redshift := config.RecommendationRecord{Provider: "aws", Service: "redshift"}
	azSP := config.RecommendationRecord{Provider: "azure", Service: "savingsplans"}
	rds := config.RecommendationRecord{Provider: "aws", Service: "rds"}
	unknown := config.RecommendationRecord{Provider: "oracle", Service: "compute"}

	tests := []struct {
		name    string
		exec    *config.PurchaseExecution
		refused bool
	}{
		{name: "NS attempt 0 failed EC2", exec: retryExec(config.StatusFailed, true, 0, ec2), refused: false},
		{name: "Redshift has no no-guard refusal to lift", exec: retryExec(config.StatusFailed, false, 0, redshift), refused: false},
		{name: "NS attempt 0 failed Azure SP", exec: retryExec(config.StatusFailed, true, 0, azSP), refused: false},
		{name: "not NS EC2", exec: retryExec(config.StatusFailed, false, 0, ec2), refused: true},
		{name: "NS attempt 1 EC2", exec: retryExec(config.StatusFailed, true, 1, ec2), refused: true},
		{name: "NS attempt 0 not failed EC2", exec: retryExec("running", true, 0, ec2), refused: true},
		{name: "NS unknown provider", exec: retryExec(config.StatusFailed, true, 0, unknown), refused: true},
		{name: "NS mixed EC2 and unknown", exec: retryExec(config.StatusFailed, true, 0, ec2, unknown), refused: true},
		{name: "RDS not NS", exec: retryExec(config.StatusFailed, false, 0, rds), refused: false},
		{name: "RDS NS", exec: retryExec(config.StatusFailed, true, 0, rds), refused: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RetryRefusalReason(tc.exec)
			if tc.refused {
				assert.NotEmpty(t, got)
			} else {
				assert.Empty(t, got)
			}
		})
	}
}

func TestRetryRefusalReasonLineageUnsentOverride(t *testing.T) {
	ec2 := config.RecommendationRecord{Provider: "aws", Service: "ec2"}
	unknown := config.RecommendationRecord{Provider: "oracle", Service: "compute"}
	e := retryExec(config.StatusFailed, false, 1, ec2)
	assert.NotEmpty(t, retryRefusalReason(e, false))
	assert.Empty(t, retryRefusalReason(e, true))
	assert.NotEmpty(t, retryRefusalReason(retryExec(config.StatusFailed, false, 1, unknown), true))
}
