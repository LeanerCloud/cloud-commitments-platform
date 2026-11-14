package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// An explicit target_coverage of 0 is a plan that buys nothing: it is stored
// and read back as 0, and only an omitted field takes the default of 80 (#523).
func TestCreatePlan_TargetCoverageZeroIsStoredAndOmittedDefaults(t *testing.T) {
	t.Parallel()
	const accountID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	for _, tc := range []struct {
		name, field string
		want        float64
	}{
		{"explicit zero", `,"target_coverage":0`, 0},
		{"omitted", ``, 80},
		{"explicit 80", `,"target_coverage":80`, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			t.Cleanup(func() { mockStore.AssertExpectations(t) })
			mockAuth := new(MockAuthService)
			t.Cleanup(func() { mockAuth.AssertExpectations(t) })
			mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
			mockAuth.grantAdminPurchaser()
			var created *config.PurchasePlan
			mockStore.On("CreatePurchasePlanWithAccounts", ctx, mock.AnythingOfType("*config.PurchasePlan"), []string{accountID}, mock.Anything).Run(func(args mock.Arguments) {
				p := *args.Get(1).(*config.PurchasePlan)
				created = &p
			}).Return(nil)
			mockStore.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
				return &config.CloudAccount{ID: id, Name: "test-aws", Provider: "aws"}, nil
			}

			h := &Handler{config: mockStore, auth: mockAuth}
			_, err := h.createPlan(ctx, &events.LambdaFunctionURLRequest{
				Headers: map[string]string{"Authorization": "Bearer admin-token"},
				Body:    `{"name":"P","enabled":true,"ramp_schedule":"immediate","provider":"aws","service":"rds","term":1,"payment":"all-upfront","target_accounts":["` + accountID + `"]` + tc.field + `}`,
			})
			require.NoError(t, err)
			require.NotNil(t, created)
			assert.Equal(t, tc.want, created.Services["aws/rds"].Coverage)

			raw, err := json.Marshal(created)
			require.NoError(t, err)
			var back config.PurchasePlan
			require.NoError(t, json.Unmarshal(raw, &back))
			assert.Equal(t, tc.want, back.Services["aws/rds"].Coverage, "the stored coverage survives the response JSON")
		})
	}
}

func TestUpdatePlan_TargetCoverageZeroIsStoredAndOmittedKeepsIt(t *testing.T) {
	t.Parallel()
	const planID = "12345678-1234-1234-1234-123456789abc"
	for _, tc := range []struct {
		name, field string
		stored      float64
		want        float64
	}{
		{"explicit zero over 65", `,"target_coverage":0`, 65, 0},
		{"omitted keeps stored zero", ``, 0, 0},
		{"explicit 80 over stored zero", `,"target_coverage":80`, 0, 80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			t.Cleanup(func() { mockStore.AssertExpectations(t) })
			mockAuth := new(MockAuthService)
			t.Cleanup(func() { mockAuth.AssertExpectations(t) })
			mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
			mockAuth.grantAdminPurchaser()
			mockStore.On("GetPurchasePlan", ctx, planID).Return(&config.PurchasePlan{
				ID: planID, Name: "Stored", RampSchedule: config.PresetRampSchedules["immediate"],
				Services: map[string]config.ServiceConfig{
					"aws/rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 1, Payment: "all-upfront", Coverage: tc.stored},
				},
			}, nil)
			var stored *config.PurchasePlan
			mockStore.On("UpdatePurchasePlan", ctx, mock.AnythingOfType("*config.PurchasePlan")).Run(func(args mock.Arguments) {
				p := *args.Get(1).(*config.PurchasePlan)
				stored = &p
			}).Return(nil)

			h := &Handler{config: mockStore, auth: mockAuth}
			_, err := h.updatePlan(ctx, &events.LambdaFunctionURLRequest{
				Headers: map[string]string{"Authorization": "Bearer admin-token"},
				Body:    `{"name":"Renamed","ramp_schedule":"immediate","provider":"aws","service":"rds"` + tc.field + `}`,
			}, planID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			assert.Equal(t, tc.want, stored.Services["aws/rds"].Coverage)
		})
	}
}
