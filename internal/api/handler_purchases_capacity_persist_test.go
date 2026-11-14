package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// capacity_percent is audit-only for the purchase maths, but it is persisted on
// the execution so History shows which fraction of the recommendation the user
// chose, and it is part of the duplicate-submit key. A stored value that
// disagreed with the request would misreport what was bought.
func TestExecutePurchase_PersistsCapacityPercent(t *testing.T) {
	t.Parallel()
	rec := `{"id": "", "provider": "aws", "service": "ec2", "resource_type": "m5.large", "term": 1, "payment": "all-upfront", "count": %d, "recommended_count": %d, "upfront_cost": 100.0, "savings": 50.0}`
	cases := []struct {
		name string
		body string
		want int
	}{
		{"an explicit capacity is stored", `{"capacity_percent": 50, "recommendations": [` + fmt.Sprintf(rec, 1, 2) + `]}`, 50},
		{"an absent capacity is stored as full", `{"recommendations": [` + fmt.Sprintf(rec, 2, 2) + `]}`, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			t.Cleanup(func() { mockStore.AssertExpectations(t) })
			mockAuth := new(MockAuthService)
			t.Cleanup(func() { mockAuth.AssertExpectations(t) })
			mockAuth.On("ValidateSession", ctx, "admin-token").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "admin@example.com"}, nil)
			mockAuth.grantAdminPurchaser()

			var saved []*config.PurchaseExecution
			mockStore.On("SavePurchaseExecution", ctx, mock.AnythingOfType("*config.PurchaseExecution")).Run(func(args mock.Arguments) {
				e := *args.Get(1).(*config.PurchaseExecution)
				saved = append(saved, &e)
			}).Return(nil)
			mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{}, nil)
			mockStore.On("GetPendingExecutions", ctx).Return([]config.PurchaseExecution{}, nil)
			expectStoredRecs(mockStore, config.RecommendationRecord{
				Provider: "aws", Service: "ec2", ResourceType: "m5.large", Count: 2, Term: 1, Payment: "all-upfront", UpfrontCost: 200, Savings: 100,
			})

			h := &Handler{config: mockStore, auth: mockAuth}
			_, err := h.executePurchase(ctx, &events.LambdaFunctionURLRequest{
				Headers: map[string]string{"Authorization": "Bearer admin-token"},
				Body:    tc.body,
			})
			require.NoError(t, err)

			require.NotEmpty(t, saved, "the execution was not saved")
			for _, e := range saved {
				assert.Equal(t, tc.want, e.CapacityPercent)
			}
		})
	}
}

// An absent or zero capacity_percent means full capacity; anything outside
// 1..100 is a client bug and is refused rather than clamped.
func TestNormalizeCapacityPercent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want int
		ok       bool
	}{
		{0, 100, true},
		{1, 1, true},
		{50, 50, true},
		{100, 100, true},
		{-1, -1, false},
		{101, 101, false},
	} {
		req := ExecutePurchaseRequest{CapacityPercent: tc.in}
		err := normalizeCapacityPercent(&req)
		if tc.ok {
			require.NoError(t, err, "%d", tc.in)
		} else {
			ce, isClient := IsClientError(err)
			require.True(t, isClient, "%d: got %v", tc.in, err)
			assert.Equal(t, 400, ce.code, "%d", tc.in)
		}
		assert.Equal(t, tc.want, req.CapacityPercent, "%d", tc.in)
	}
}
