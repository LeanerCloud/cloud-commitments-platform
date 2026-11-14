package api

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

// armedBoundPlan is an armed bound plan with one group g1.
func armedBoundPlan() *config.PurchasePlan {
	p := apgStoredPlan(true, apgCap(1000))
	p.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	p.Services = map[string]config.ServiceConfig{"aws/ec2#g1": {
		Provider: "aws", Service: "ec2", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80,
		MinCount: 1, GroupID: "g1", BoundIdentities: []string{"k1", "k2"}, MaxStepCommitment: apgCap(100),
	}}
	return p
}

// edited returns a copy of the armed bound plan with f applied to group g1.
func edited(f func(p *config.PurchasePlan, g *config.ServiceConfig)) *config.PurchasePlan {
	p := armedBoundPlan()
	g := p.Services["aws/ec2#g1"]
	f(p, &g)
	p.Services["aws/ec2#g1"] = g
	return p
}

func TestPlanArmEscalates_BoundGroups(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		after *config.PurchasePlan
		name  string
		want  bool
	}{
		{name: "unchanged", after: armedBoundPlan(), want: false},
		{name: "narrowed bound set", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.BoundIdentities = []string{"k1"} }), want: false},
		{name: "lowered group cap", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.MaxStepCommitment = apgCap(50) }), want: false},
		{name: "coverage edit", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.Coverage = 60 }), want: false},
		{name: "new group", after: edited(func(p *config.PurchasePlan, _ *config.ServiceConfig) {
			p.Services["aws/ec2#g2"] = config.ServiceConfig{Provider: "aws", Service: "ec2", Enabled: true, Term: 3, Payment: "all-upfront", GroupID: "g2", BoundIdentities: []string{"k9"}}
		}), want: true},
		{name: "added bound identity", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.BoundIdentities = []string{"k1", "k2", "k3"} }), want: true},
		{name: "bound to unbound", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.BoundIdentities = nil }), want: true},
		{name: "raised group cap", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.MaxStepCommitment = apgCap(200) }), want: true},
		{name: "removed group cap (nil is unbounded)", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.MaxStepCommitment = nil }), want: true},
		{name: "term changed", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.Term = 3 }), want: true},
		{name: "payment changed", after: edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.Payment = "all-upfront" }), want: true},
		{name: "marker removed", after: edited(func(p *config.PurchasePlan, _ *config.ServiceConfig) { p.RampSchedule.AllocationMode = "" }), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, planArmEscalates(armedBoundPlan(), tc.after))
		})
	}
	t.Run("re-enabling a disabled group", func(t *testing.T) {
		before := edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.Enabled = false })
		assert.True(t, planArmEscalates(before, armedBoundPlan()))
		assert.False(t, planArmEscalates(armedBoundPlan(), before), "disabling a group narrows")
	})
	t.Run("arming a disarmed bound plan escalates even when it narrows", func(t *testing.T) {
		disarmed := armedBoundPlan()
		disarmed.AutoPurchase = false
		assert.True(t, planArmEscalates(disarmed, armedBoundPlan()), "unchanged")
		narrowed := edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.BoundIdentities = []string{"k1"} })
		assert.True(t, planArmEscalates(disarmed, narrowed), "narrowed")
	})
	t.Run("a disabled group buys nothing, so its edits wait for re-enabling", func(t *testing.T) {
		withG2 := func(enabled bool, ids ...string) *config.PurchasePlan {
			return edited(func(p *config.PurchasePlan, _ *config.ServiceConfig) {
				p.Services["aws/ec2#g2"] = config.ServiceConfig{Provider: "aws", Service: "ec2", Enabled: enabled, Term: 1, Payment: "no-upfront", GroupID: "g2", BoundIdentities: ids}
			})
		}
		assert.False(t, planArmEscalates(armedBoundPlan(), withG2(false, "k9")), "new disabled group")
		assert.False(t, planArmEscalates(withG2(false, "k9"), withG2(false, "k9", "k8")), "disabled group widened")
		assert.True(t, planArmEscalates(withG2(false, "k9"), withG2(true, "k9")), "enabling it")
		assert.True(t, planArmEscalates(armedBoundPlan(), withG2(true, "k9")), "new enabled group")
	})
	t.Run("a group cap added where none was set narrows", func(t *testing.T) {
		before := edited(func(_ *config.PurchasePlan, g *config.ServiceConfig) { g.MaxStepCommitment = nil })
		assert.False(t, planArmEscalates(before, armedBoundPlan()))
	})
}

// (guard) Unmarked armed plans keep today's rules: a term- or payment-only
// edit is not an escalation, so admin-key callers are unaffected.
func TestPlanArmEscalates_UnmarkedTermAndPaymentEditsDoNotEscalate(t *testing.T) {
	t.Parallel()
	plan := func(term int, payment string) *config.PurchasePlan {
		p := apgStoredPlan(true, apgCap(1000))
		p.Services = map[string]config.ServiceConfig{"aws/rds": {Provider: "aws", Service: "rds", Enabled: true, Term: term, Payment: payment}}
		return p
	}
	assert.False(t, planArmEscalates(plan(1, "no-upfront"), plan(3, "no-upfront")))
	assert.False(t, planArmEscalates(plan(1, "no-upfront"), plan(1, "all-upfront")))
}
