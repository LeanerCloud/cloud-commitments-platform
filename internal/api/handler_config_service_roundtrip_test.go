package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// PUT /api/config/service/{provider}/{service} must store every field the body
// carries from the overlay allowlist, take the provider and service from the URL
// whatever the body says, and leave an existing row's ramp_schedule alone (it is
// set out-of-band; a new record stores the whole body).
// The older tests save through mock.Anything or check one field at a time.
func TestUpdateServiceConfig_StoresEveryOverlayFieldFromTheBody(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "uid"}, nil)
	mockAuth.grantAdmin()

	stored := &config.ServiceConfig{
		Provider: "aws", Service: "rds", Enabled: true, Term: 3, Payment: "all-upfront", Coverage: 50,
		RampSchedule:   "weekly-25pct",
		IncludeEngines: []string{"mysql"}, ExcludeEngines: []string{"oracle"},
		IncludeRegions: []string{"us-east-1"}, ExcludeRegions: []string{"us-west-2"},
		IncludeTypes: []string{"db.m5.large"}, ExcludeTypes: []string{"db.t2.micro"},
		MinCount: 4,
	}
	mockStore.On("GetServiceConfig", ctx, "aws", "rds").Return(stored, nil)
	var saved *config.ServiceConfig
	mockStore.On("SaveServiceConfig", ctx, mock.AnythingOfType("*config.ServiceConfig")).Run(func(args mock.Arguments) {
		c := *args.Get(1).(*config.ServiceConfig)
		saved = &c
	}).Return(nil)

	h := &Handler{config: mockStore, auth: mockAuth}
	_, err := h.updateServiceConfig(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer admin-token"},
		Body: `{"provider":"azure","service":"vm","enabled":false,"term":1,"payment":"no-upfront","coverage":70,
			"ramp_schedule":"monthly-10pct",
			"include_engines":["postgres"],"exclude_engines":["mariadb"],
			"include_regions":["eu-west-1"],"exclude_regions":["ap-south-1"],
			"include_types":["db.r5.large"],"exclude_types":["db.t3.micro"],
			"min_count":9}`,
	}, "aws/rds")
	require.NoError(t, err)

	require.NotNil(t, saved, "nothing was saved")
	assert.Equal(t, "aws", saved.Provider, "provider comes from the URL")
	assert.Equal(t, "rds", saved.Service, "service comes from the URL")
	assert.False(t, saved.Enabled, "enabled")
	assert.Equal(t, 1, saved.Term, "term")
	assert.Equal(t, "no-upfront", saved.Payment, "payment")
	assert.Equal(t, 70.0, saved.Coverage, "coverage")
	assert.Equal(t, []string{"postgres"}, saved.IncludeEngines, "include_engines")
	assert.Equal(t, []string{"mariadb"}, saved.ExcludeEngines, "exclude_engines")
	assert.Equal(t, []string{"eu-west-1"}, saved.IncludeRegions, "include_regions")
	assert.Equal(t, []string{"ap-south-1"}, saved.ExcludeRegions, "exclude_regions")
	assert.Equal(t, []string{"db.r5.large"}, saved.IncludeTypes, "include_types")
	assert.Equal(t, []string{"db.t3.micro"}, saved.ExcludeTypes, "exclude_types")
	assert.Equal(t, 9, saved.MinCount, "min_count")
	assert.Equal(t, "weekly-25pct", saved.RampSchedule, "an existing row's ramp_schedule is not overlaid from the body")
}
