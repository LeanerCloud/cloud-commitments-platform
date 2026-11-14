package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestApplyEngineFallback covers the engine-backfill helper directly so a
// future refactor that changes which *Details types carry an Engine field
// surfaces here rather than via a silent mis-purchase regression.
func TestApplyEngineFallback(t *testing.T) {
	// RDS: empty Engine should be backfilled.
	rds := &common.DatabaseDetails{}
	applyEngineFallback(rds, "mysql")
	assert.Equal(t, "mysql", rds.Engine, "empty DB Engine must be backfilled from the record column")

	// RDS: pre-populated Engine must NOT be overwritten — Details is
	// the source of truth when present.
	rdsKeep := &common.DatabaseDetails{Engine: "postgres"}
	applyEngineFallback(rdsKeep, "mysql")
	assert.Equal(t, "postgres", rdsKeep.Engine, "non-empty DB Engine must be preserved")

	// Cache: same rule.
	ec := &common.CacheDetails{}
	applyEngineFallback(ec, "redis")
	assert.Equal(t, "redis", ec.Engine)

	// Empty engine arg is a no-op.
	rdsZero := &common.DatabaseDetails{}
	applyEngineFallback(rdsZero, "")
	assert.Empty(t, rdsZero.Engine, "empty engine arg must leave Details untouched")

	// Non-DB/Cache types are no-ops (don't accidentally write into them).
	cc := &common.ComputeDetails{}
	applyEngineFallback(cc, "mysql")
	assert.Empty(t, cc.Platform, "ComputeDetails must remain untouched by applyEngineFallback")
}

// The stored per-account execution error is visible to users scoped to the
// tenant account, so a failed host-identity check must not expose the host
// account id or the raw STS error.
func TestExecuteForAccount_NonHostRoleARN_StoredErrorOmitsHostIdentity(t *testing.T) {
	ctx := context.Background()
	const hostID = "111111111111"
	hostSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, hostSTS)
	hostSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(&sts.GetCallerIdentityOutput{Account: aws.String(hostID)}, nil)

	var saved []*config.PurchaseExecution
	mockStore := new(MockConfigStore)
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		saved = append(saved, e)
		return nil
	}
	m := &Manager{
		config:          mockStore,
		assumeRoleSTS:   new(MockAssumeRoleSTS),
		stsClient:       hostSTS,
		ambientAWSCreds: aws.AnonymousCredentials{},
	}
	account := config.CloudAccount{ID: "tenant", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "role_arn"}
	baseExec := &config.PurchaseExecution{
		ExecutionID:     "exec-nonhost",
		PlanID:          "plan-nonhost",
		Recommendations: []config.RecommendationRecord{{Provider: "aws", Service: "ec2", Count: 1, Selected: true}},
	}

	committed, err := m.executeForAccount(ctx, baseExec, &config.PurchasePlan{ID: "plan-nonhost"}, account)
	require.ErrorIs(t, err, credentials.ErrNotHostAccount)
	assert.False(t, committed)
	require.NotEmpty(t, saved)
	for _, e := range saved {
		assert.NotContains(t, e.Error, hostID)
		assert.NotContains(t, e.Error, "GetCallerIdentity")
	}
	assert.Contains(t, saved[len(saved)-1].Error, credentials.ErrNotHostAccount.Error())
}
