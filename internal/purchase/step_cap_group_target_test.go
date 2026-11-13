package purchase

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/require"
)

// A merged group cap and a merged plan cap each refuse a step over them.
func TestMergedGroupTargetCapsRefuseOverEffectiveCap(t *testing.T) {
	rec := capRec("us-east-1", 100, "")
	g := func(c float64) config.ServiceConfig { return group(usd(c), identity(t, rec)) }
	plan := boundCapPlan(true, usd(500), map[string]config.ServiceConfig{"aws/ec2#a": func() config.ServiceConfig { s := g(500); s.GroupID = "a"; return s }()})
	recs := []config.RecommendationRecord{rec}
	require.NoError(t, CheckStepCommitmentCap(plan, recs))

	byGroup, err := config.MergePlanGroupTarget(plan, config.PlanGroupTarget{GroupID: "a", MaxStepCommitment: usd(50)})
	require.NoError(t, err)
	require.ErrorIs(t, CheckStepCommitmentCap(byGroup, recs), ErrStepCommitmentCap)

	byPlan, err := config.MergePlanGroupTarget(plan, config.PlanGroupTarget{GroupID: "a", PlanMaxStepCommitment: usd(50)})
	require.NoError(t, err)
	require.ErrorIs(t, CheckStepCommitmentCap(byPlan, recs), ErrStepCommitmentCap)
}
