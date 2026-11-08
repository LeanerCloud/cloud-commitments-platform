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
		{"NS attempt 0 failed EC2", retryExec(config.StatusFailed, true, 0, ec2), false},
		{"Redshift has no no-guard refusal to lift", retryExec(config.StatusFailed, false, 0, redshift), false},
		{"NS attempt 0 failed Azure SP", retryExec(config.StatusFailed, true, 0, azSP), false},
		{"not NS EC2", retryExec(config.StatusFailed, false, 0, ec2), true},
		{"NS attempt 1 EC2", retryExec(config.StatusFailed, true, 1, ec2), true},
		{"NS attempt 0 not failed EC2", retryExec("running", true, 0, ec2), true},
		{"NS unknown provider", retryExec(config.StatusFailed, true, 0, unknown), true},
		{"NS mixed EC2 and unknown", retryExec(config.StatusFailed, true, 0, ec2, unknown), true},
		{"RDS not NS", retryExec(config.StatusFailed, false, 0, rds), false},
		{"RDS NS", retryExec(config.StatusFailed, true, 0, rds), false},
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
