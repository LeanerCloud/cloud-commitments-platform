package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func groupRec(id, provider, service, region, typ string, term int, payment, account string) config.RecommendationRecord {
	return config.RecommendationRecord{
		ID: id, Provider: provider, Service: service, Region: region, ResourceType: typ,
		Term: term, Payment: payment, CloudAccountID: &account, Count: 2,
	}
}

func identityOf(t *testing.T, rec config.RecommendationRecord) string {
	t.Helper()
	key, err := config.RecIdentityKey(&rec)
	require.NoError(t, err)
	return key
}

func TestBuildBoundServices_TwoGroups(t *testing.T) {
	t.Parallel()
	ec2a := groupRec("a", "aws", "ec2", "us-east-1", "m5.large", 3, "all-upfront", "acct-1")
	ec2b := groupRec("b", "aws", "ec2", "us-west-2", "m5.large", 3, "all-upfront", "acct-1")
	az := groupRec("c", "azure", "compute", "westeurope", "Standard_D2s_v3", 1, "monthly", "acct-2")
	groups := []PlanGroupRequest{
		{Provider: "aws", Service: "ec2", Term: ptr(3), Payment: ptr("all-upfront"), Coverage: ptr(70), RecommendationIDs: []string{"a", "b"}},
		{Provider: "azure", Service: "compute", Term: ptr(1), Payment: ptr("no-upfront"), MaxStepCommitment: ptr(250.0), RecommendationIDs: []string{"c"}},
	}
	got, err := buildBoundServices(groups, [][]config.RecommendationRecord{{ec2a, ec2b}, {az}})
	require.NoError(t, err)
	require.Len(t, got, 2)

	byProvider := map[string]config.ServiceConfig{}
	for key, svc := range got {
		assert.Equal(t, svc.Provider+"/"+svc.Service+"#"+svc.GroupID, key)
		assert.NotEmpty(t, svc.GroupID)
		assert.True(t, svc.Enabled)
		assert.Equal(t, 1, svc.MinCount, "rollback sentinel")
		byProvider[svc.Provider] = svc
	}
	assert.NotEqual(t, byProvider["aws"].GroupID, byProvider["azure"].GroupID)

	aws := byProvider["aws"]
	assert.Equal(t, 3, aws.Term)
	assert.Equal(t, "all-upfront", aws.Payment)
	assert.Equal(t, 70.0, aws.Coverage)
	assert.Nil(t, aws.MaxStepCommitment)
	assert.Equal(t, []string{identityOf(t, ec2a), identityOf(t, ec2b)}, aws.BoundIdentities)

	azure := byProvider["azure"]
	assert.Equal(t, "monthly", azure.Payment, "azure payment normalized to its canonical token")
	assert.Equal(t, float64(config.DefaultCoveragePercent), azure.Coverage)
	require.NotNil(t, azure.MaxStepCommitment)
	assert.Equal(t, 250.0, *azure.MaxStepCommitment)
	assert.Equal(t, []string{identityOf(t, az)}, azure.BoundIdentities)
}

func TestBuildBoundServices_DefaultsAndErrors(t *testing.T) {
	t.Parallel()
	rec := groupRec("a", "aws", "ec2", "us-east-1", "m5.large", 3, "no-upfront", "acct-1")
	got, err := buildBoundServices([]PlanGroupRequest{{Provider: "aws", Service: "ec2", RecommendationIDs: []string{"a"}}},
		[][]config.RecommendationRecord{{rec}})
	require.NoError(t, err)
	for _, svc := range got {
		assert.Equal(t, 3, svc.Term)
		assert.Equal(t, "no-upfront", svc.Payment)
	}

	_, err = buildBoundServices([]PlanGroupRequest{{Provider: "aws", Service: "ec2", Payment: ptr("bogus"), RecommendationIDs: []string{"a"}}},
		[][]config.RecommendationRecord{{rec}})
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Contains(t, ce.message, "groups[0].payment")

	_, err = buildBoundServices([]PlanGroupRequest{{Provider: "aws", Service: "ec2"}}, nil)
	assert.Error(t, err)
}

func TestBuildBoundServices_DuplicateIdentityAcrossGroupsFailsValidate(t *testing.T) {
	t.Parallel()
	rec := groupRec("a", "aws", "ec2", "us-east-1", "m5.large", 3, "all-upfront", "acct-1")
	groups := []PlanGroupRequest{
		{Provider: "aws", Service: "ec2", Term: ptr(3), Payment: ptr("all-upfront"), RecommendationIDs: []string{"a"}},
		{Provider: "aws", Service: "ec2", Term: ptr(3), Payment: ptr("all-upfront"), RecommendationIDs: []string{"a"}},
	}
	services, err := buildBoundServices(groups, [][]config.RecommendationRecord{{rec}, {rec}})
	require.NoError(t, err)
	plan := &config.PurchasePlan{Name: "p", Services: services, RampSchedule: config.PresetRampSchedules["immediate"]}
	plan.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	assert.ErrorIs(t, plan.Validate(), config.ErrInvalidBoundGroup)
}
