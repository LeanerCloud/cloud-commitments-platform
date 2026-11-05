package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

func displayPlan(marked bool, entries map[string]config.ServiceConfig) *config.PurchasePlan {
	p := &config.PurchasePlan{ID: "plan-d", Name: "d", Services: entries, RampSchedule: config.RampSchedule{TotalSteps: 2}}
	if marked {
		p.RampSchedule.AllocationMode = config.RampAllocationBoundGroups
	}
	return p
}

func displayEntry(provider, service string, term int, payment string) config.ServiceConfig {
	return config.ServiceConfig{Provider: provider, Service: service, Enabled: true, Term: term, Payment: payment}
}

func TestPlanDisplayFor(t *testing.T) {
	ec2 := displayEntry("aws", "ec2", 1, "no-upfront")
	rds := displayEntry("aws", "rds", 1, "no-upfront")
	vm := displayEntry("azure", "compute", 3, "monthly")

	t.Run("same provider keeps it; differing service is multiple", func(t *testing.T) {
		d := planDisplayFor(displayPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": ec2, "aws/rds#g2": rds}))
		assert.Equal(t, "aws", d.svc.Provider)
		assert.Equal(t, "multiple", d.svc.Service)
		assert.Equal(t, 1, d.svc.Term)
		assert.Equal(t, "no-upfront", d.svc.Payment)
		assert.Equal(t, []string{"aws"}, d.providers)
	})
	t.Run("cross-provider is multiple with term 0", func(t *testing.T) {
		d := planDisplayFor(displayPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": ec2, "azure/compute#g2": vm}))
		assert.Equal(t, "multiple", d.svc.Provider)
		assert.Equal(t, "multiple", d.svc.Service)
		assert.Equal(t, 0, d.svc.Term)
		assert.Equal(t, "multiple", d.svc.Payment)
		assert.Equal(t, []string{"aws", "azure"}, d.providers)
	})
	t.Run("disabled groups are left out", func(t *testing.T) {
		off := vm
		off.Enabled = false
		d := planDisplayFor(displayPlan(true, map[string]config.ServiceConfig{"aws/ec2#g1": ec2, "azure/compute#g2": off}))
		assert.Equal(t, "aws", d.svc.Provider)
		assert.Equal(t, "ec2", d.svc.Service)
		assert.Equal(t, []string{"aws"}, d.providers)
	})
	t.Run("(guard) an unmarked multi-service plan shows its first entry, no providers", func(t *testing.T) {
		d := planDisplayFor(displayPlan(false, map[string]config.ServiceConfig{"aws/ec2": ec2, "azure/compute": vm}))
		assert.Equal(t, ec2, d.svc)
		assert.Nil(t, d.providers)
	})
}

func TestPlanRows_CarryBoundProviders(t *testing.T) {
	exec := &config.PurchaseExecution{ExecutionID: "e1", PlanID: "plan-d", ScheduledDate: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	bound := displayPlan(true, map[string]config.ServiceConfig{
		"aws/ec2#g1":       displayEntry("aws", "ec2", 1, "no-upfront"),
		"azure/compute#g2": displayEntry("azure", "compute", 3, "monthly"),
	})
	up := upcomingFromExecution(bound, exec)
	assert.Equal(t, "multiple", up.Provider)
	assert.Equal(t, []string{"aws", "azure"}, up.Providers)
	planned := buildPlannedPurchase(bound, exec)
	assert.Equal(t, []string{"aws", "azure"}, planned.Providers)
	assert.Equal(t, 0, planned.Term)

	// (guard) an unmarked plan's rows keep today's JSON: no providers key.
	legacy := displayPlan(false, map[string]config.ServiceConfig{"aws/ec2": displayEntry("aws", "ec2", 1, "no-upfront")})
	for _, row := range []any{upcomingFromExecution(legacy, exec), buildPlannedPurchase(legacy, exec)} {
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), `"providers"`)
	}
}

func TestUpcomingFilter_MatchesAnyBoundProvider(t *testing.T) {
	row := &UpcomingPurchase{Provider: "multiple", Providers: []string{"aws", "azure"}}
	assert.True(t, rowHasProvider(row, "azure"))
	assert.True(t, rowHasProvider(row, "AWS"))
	assert.False(t, rowHasProvider(row, "gcp"))
	// (guard) an unmarked row matches on its provider alone, as before.
	assert.True(t, rowHasProvider(&UpcomingPurchase{Provider: "aws"}, "aws"))
	assert.False(t, rowHasProvider(&UpcomingPurchase{Provider: "aws"}, "azure"))
}
