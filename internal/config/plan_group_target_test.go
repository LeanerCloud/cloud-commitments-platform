package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func fp(v float64) *float64 { return &v }

func boundTwoGroupPlan() *PurchasePlan {
	p := &PurchasePlan{
		Services: map[string]ServiceConfig{
			"aws/ec2#a": {Provider: "aws", Service: "ec2", GroupID: "a", Enabled: true, MaxStepCommitment: fp(100), BoundIdentities: []string{"x"}},
			"aws/rds#b": {Provider: "aws", Service: "rds", GroupID: "b", Enabled: true, MaxStepCommitment: fp(200), BoundIdentities: []string{"y"}},
		},
		MaxStepCommitment: fp(500),
	}
	p.RampSchedule.AllocationMode = RampAllocationBoundGroups
	return p
}

func TestMergePlanGroupTargetEditsOnlyThatGroup(t *testing.T) {
	plan := boundTwoGroupPlan()
	got, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "b", MaxStepCommitment: fp(150)})
	require.NoError(t, err)
	require.Equal(t, 150.0, *got.Services["aws/rds#b"].MaxStepCommitment)
	require.Equal(t, plan.Services["aws/ec2#a"], got.Services["aws/ec2#a"])
	require.Equal(t, RampAllocationBoundGroups, got.RampSchedule.AllocationMode)
	require.Equal(t, 200.0, *plan.Services["aws/rds#b"].MaxStepCommitment, "input plan is not mutated")
}

func TestMergePlanGroupTargetCapsOnlyStayOrDrop(t *testing.T) {
	plan := boundTwoGroupPlan()
	_, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(100)})
	require.NoError(t, err, "equal is allowed")
	_, err = MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", PlanMaxStepCommitment: fp(500)})
	require.NoError(t, err)
	for name, edit := range map[string]PlanGroupTarget{
		"group raise": {GroupID: "a", MaxStepCommitment: fp(101)},
		"plan raise":  {GroupID: "a", PlanMaxStepCommitment: fp(501)},
		"zero":        {GroupID: "a", MaxStepCommitment: fp(0)},
		"nan":         {GroupID: "a", MaxStepCommitment: fp(nanValue())},
		"group inf":   {GroupID: "a", MaxStepCommitment: fp(math.Inf(1))},
		"plan inf":    {GroupID: "a", PlanMaxStepCommitment: fp(math.Inf(1))},
	} {
		_, err := MergePlanGroupTarget(plan, edit)
		require.ErrorIs(t, err, ErrGroupTargetRefused, name)
	}
}

func TestMergePlanGroupTargetEffectiveCapCannotRise(t *testing.T) {
	plan := boundTwoGroupPlan()
	plan.Services["aws/ec2#a"] = func() ServiceConfig { s := plan.Services["aws/ec2#a"]; s.MaxStepCommitment = nil; return s }()
	plan.MaxStepCommitment = fp(50)
	_, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(80)})
	require.NoError(t, err, "a group cap above the plan cap leaves the effective cap at 50")
	got, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(30)})
	require.NoError(t, err)
	require.Equal(t, 30.0, EffectiveStepCap(got.MaxStepCommitment, got.Services["aws/ec2#a"].MaxStepCommitment))
	require.Equal(t, 50.0, EffectiveStepCap(plan.MaxStepCommitment, plan.Services["aws/ec2#a"].MaxStepCommitment))
}

func TestMergePlanGroupTargetRefusals(t *testing.T) {
	_, err := MergePlanGroupTarget(boundTwoGroupPlan(), PlanGroupTarget{GroupID: "nope"})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	_, err = MergePlanGroupTarget(boundTwoGroupPlan(), PlanGroupTarget{})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	legacy := boundTwoGroupPlan()
	legacy.RampSchedule.AllocationMode = ""
	_, err = MergePlanGroupTarget(legacy, PlanGroupTarget{GroupID: "a"})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	_, err = MergePlanGroupTarget(nil, PlanGroupTarget{GroupID: "a"})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
}

func nanValue() float64 { var z float64; return z / z }

func TestMergePlanGroupTargetWritesPlanCap(t *testing.T) {
	got, err := MergePlanGroupTarget(boundTwoGroupPlan(), PlanGroupTarget{GroupID: "a", PlanMaxStepCommitment: fp(400)})
	require.NoError(t, err)
	require.Equal(t, 400.0, *got.MaxStepCommitment)
}

func TestMergePlanGroupTargetRefusesDuplicateGroupID(t *testing.T) {
	plan := boundTwoGroupPlan()
	plan.Services["aws:ec2#a"] = ServiceConfig{Provider: "aws", Service: "ec2", GroupID: "a", Enabled: true, MaxStepCommitment: fp(100)}
	_, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(50)})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
}

func TestMergePlanGroupTargetRefusesPlanCapOnUncappedAutoPlan(t *testing.T) {
	plan := boundTwoGroupPlan()
	plan.AutoPurchase = true
	plan.MaxStepCommitment = nil
	_, err := MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", PlanMaxStepCommitment: fp(50)})
	require.ErrorIs(t, err, ErrGroupTargetRefused)
	_, err = MergePlanGroupTarget(plan, PlanGroupTarget{GroupID: "a", MaxStepCommitment: fp(50)})
	require.NoError(t, err, "group cap behavior is unchanged")
}
