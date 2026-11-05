package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Fixtures are hand-built plans, not real configuration (issue #1746).

func TestDerivePlanServices_CutsBoundGroupSuffix(t *testing.T) {
	p := &PurchasePlan{Services: map[string]ServiceConfig{
		"aws/ec2#g1": {}, "aws/ec2#g2": {}, "aws/rds#g3": {},
	}}
	assert.Equal(t, []string{"ec2", "rds"}, DerivePlanServices(p))
	providers, err := DerivePlanProviders(p)
	assert.NoError(t, err)
	assert.Equal(t, []string{"aws"}, providers)
	// (guard) legacy keys are unchanged.
	legacy := &PurchasePlan{Services: map[string]ServiceConfig{"aws/ec2": {}, "aws:savings-plans-compute": {}}}
	assert.Equal(t, []string{"ec2", "savings-plans-compute"}, DerivePlanServices(legacy))
}
