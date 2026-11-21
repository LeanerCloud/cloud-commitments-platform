package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1959: capacity_percent scales a Savings Plans rec's hourly commitment in
// priceFromStored, once; everything downstream reads the persisted rec.

const spCapacitySlug = "savings-plans-compute"

// spStored is a 1-year all-upfront SP row: hourly 1.00, upfront 5000, monthly
// 100, so the spend-cap total (upfront + monthly*term*12) is 6200 at 100%.
func spStored(details string) config.RecommendationRecord {
	return config.RecommendationRecord{
		ID: "sp-stored", Provider: "aws", Service: spCapacitySlug, Region: "us-east-1",
		Count: 1, Term: 1, Payment: "all-upfront",
		UpfrontCost: 5000, MonthlyCost: float64Ptr(100), Savings: 800, OnDemandCost: float64Ptr(200), CommitmentCurrency: "USD",
		Details: json.RawMessage(details),
	}
}

const spDetails = `{"plan_type":"Compute","hourly_commitment":1,"offering_id":"off-1"}`

func spReqJSON(capacity int, extra string) string {
	return fmt.Sprintf(`{"capacity_percent":%d,"recommendations":[{"id":"","provider":"aws","service":"%s","region":"us-east-1","count":1,"term":1,"payment":"all-upfront","upfront_cost":1,"savings":1,"selected":true,"details":{"hourly_commitment":99},"recommended_count":0}%s]}`,
		capacity, spCapacitySlug, extra)
}

// runSPExecute drives the real executePurchase handler against a strict store
// and returns the persisted execution (nil when refused).
func runSPExecute(t *testing.T, cap float64, body string, stored ...config.RecommendationRecord) (*config.PurchaseExecution, error) {
	t.Helper()
	return runSPExecuteWith(t, &recordingEmailNotifier{}, cap, body, stored...)
}

func runSPExecuteWith(t *testing.T, notifier *recordingEmailNotifier, cap float64, body string, stored ...config.RecommendationRecord) (*config.PurchaseExecution, error) {
	t.Helper()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	mockAuth.On("ValidateSession", ctx, "tok").Return(&Session{UserID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", Email: "a@example.com"}, nil)
	if cap > 0 {
		mockAuth.grantPermissions([]auth.Permission{
			{Action: auth.ActionExecute, Resource: auth.ResourcePurchases, Constraints: &auth.PermissionConstraints{MaxPurchaseAmount: cap}},
			{Action: auth.ActionExecuteAny, Resource: auth.ResourcePurchases},
		})
	} else {
		mockAuth.grantAdminPurchaser()
	}
	expectStoredRecs(mockStore, stored...)
	notify := "n@example.com"
	mockStore.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{NotificationEmail: &notify}, nil).Maybe()
	mockStore.On("GetPendingExecutions", mock.Anything).Return([]config.PurchaseExecution{}, nil).Maybe()
	var saved *config.PurchaseExecution
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { c := *args.Get(1).(*config.PurchaseExecution); saved = &c }).
		Return(nil).Maybe()
	h := &Handler{config: mockStore, auth: mockAuth, emailNotifier: notifier}
	_, err := h.executePurchase(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer tok"}, Body: body,
	})
	if err != nil {
		assert.Nil(t, saved, "a refused request must not persist an execution")
	}
	return saved, err
}

func hourlyOf(t *testing.T, raw json.RawMessage) float64 {
	t.Helper()
	var d struct {
		Hourly float64 `json:"hourly_commitment"`
	}
	require.NoError(t, json.Unmarshal(raw, &d))
	return d.Hourly
}

func TestExecutePurchase_SavingsPlanHalfCapacityBuysHalfOnce(t *testing.T) {
	t.Parallel()
	saved, err := runSPExecute(t, 0, spReqJSON(50, ""), spStored(spDetails))
	require.NoError(t, err)
	require.Len(t, saved.Recommendations, 1)
	rec := saved.Recommendations[0]
	assert.Equal(t, 1, rec.Count)
	assert.Equal(t, 0.5, hourlyOf(t, rec.Details), "client-sent details.hourly_commitment must be ignored")
	assert.JSONEq(t, `{"plan_type":"Compute","hourly_commitment":0.5,"offering_id":"off-1"}`, string(rec.Details))
	assert.Equal(t, 2500.0, rec.UpfrontCost)
	assert.Equal(t, 400.0, rec.Savings)
	assert.Equal(t, 50.0, *rec.MonthlyCost)
	assert.Equal(t, 100.0, *rec.OnDemandCost)
	assert.Equal(t, 50, saved.CapacityPercent)
	assert.Equal(t, 2500.0, saved.TotalUpfrontCost, "persisted total == priced amount")

	// The persisted rec is what the real executor buys: exactly once, 0.50/hr.
	svc := runSuccessorThroughManager(t, saved)
	assert.Equal(t, []float64{0.5}, svc.hourly)

	// Retry replays the persisted scaled rec: no store lookup (the strict
	// handler mock has no ListStoredRecommendations), no rescale.
	failed := *saved
	failed.Status = "failed"
	creator := retryCallerID
	failed.CreatedByUserID = &creator
	handler, mockConfig, _ := buildSessionRetryHandler(t, &failed, &Session{UserID: retryCallerID, Email: "op@example.com"}, false, true)
	var successor *config.PurchaseExecution
	mockConfig.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).
		Run(func(args mock.Arguments) { c := *args.Get(1).(*config.PurchaseExecution); successor = &c }).Return(nil).Maybe()
	_, err = handler.retryPurchase(context.Background(), sessionRetryReqWithForce(), failed.ExecutionID)
	require.NoError(t, err)
	require.NotNil(t, successor)
	assert.Equal(t, 50, successor.CapacityPercent)
	assert.Equal(t, 0.5, hourlyOf(t, successor.Recommendations[0].Details))
	assert.Equal(t, []float64{0.5}, runSuccessorThroughManager(t, successor).hourly)
}

func TestExecutePurchase_SavingsPlanCapacity100Unchanged(t *testing.T) {
	t.Parallel()
	odd := `{"plan_type":"Compute","hourly_commitment":1.23456}`
	saved, err := runSPExecute(t, 0, spReqJSON(100, ""), spStored(odd))
	require.NoError(t, err)
	rec := saved.Recommendations[0]
	assert.Equal(t, odd, string(rec.Details), "no floor and no rewrite at 100%")
	assert.Equal(t, 5000.0, rec.UpfrontCost)
	assert.Equal(t, 100.0, *rec.MonthlyCost)
	assert.Equal(t, 800.0, rec.Savings)
}

func TestExecutePurchase_SavingsPlanSpendCapSeesScaledTotal(t *testing.T) {
	t.Parallel()
	// 100% total is 6200, 50% total is 3100.
	_, err := runSPExecute(t, 4000, spReqJSON(50, ""), spStored(spDetails))
	require.NoError(t, err, "a cap that blocks 100% allows 50%")

	_, err = runSPExecute(t, 4000, spReqJSON(100, ""), spStored(spDetails))
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 403, ce.code)

	_, err = runSPExecute(t, 3000, spReqJSON(50, ""), spStored(spDetails))
	ce, ok = IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 403, ce.code, "a cap below the scaled total still blocks")
}

func TestExecutePurchase_SavingsPlanBelowMinimumRefusesWholeRequest(t *testing.T) {
	t.Parallel()
	ri := config.RecommendationRecord{
		ID: "ri", Provider: "aws", Service: "ec2", Region: "us-east-1", ResourceType: "m5.large",
		Count: 10, Term: 1, Payment: "all-upfront", UpfrontCost: 1000, Savings: 100,
	}
	riReq := `,{"id":"","provider":"aws","service":"ec2","region":"us-east-1","resource_type":"m5.large","count":5,"term":1,"payment":"all-upfront","selected":true}`
	_, err := runSPExecute(t, 0, spReqJSON(5, riReq), spStored(`{"plan_type":"Compute","hourly_commitment":0.10}`), ri)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 400, ce.code)
	assert.Contains(t, ce.Error(), "recommendation 0")
	assert.Contains(t, ce.Error(), spCapacitySlug)
	assert.Contains(t, ce.Error(), "$0.01/hr minimum")
}

func TestPriceFromStored_SavingsPlanFloorsNeverRoundsUp(t *testing.T) {
	t.Parallel()
	for cents := 1; cents <= 250; cents++ {
		stored := spStored(fmt.Sprintf(`{"hourly_commitment":%s}`, centsString(cents)))
		for pct := 1; pct <= 99; pct++ {
			req := config.RecommendationRecord{Count: 1, Service: spCapacitySlug}
			out, err := priceFromStored(&req, &stored, 0, pct)
			wantCents := cents * pct / 100
			if wantCents < 1 {
				ce, ok := IsClientError(err)
				require.True(t, ok, "%dc at %d%%: %v", cents, pct, err)
				assert.Equal(t, 400, ce.code)
				continue
			}
			require.NoError(t, err, "%dc at %d%%", cents, pct)
			assert.Equal(t, float64(wantCents)/100, hourlyOf(t, out.Details), "%dc at %d%%", cents, pct)
			assert.Equal(t, 1, out.Count)
			assert.InDelta(t, 5000*float64(wantCents)/float64(cents), out.UpfrontCost, 1e-6)
		}
	}
}

func centsString(c int) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

func TestPriceFromStored_SavingsPlanCapacityFailureModes(t *testing.T) {
	t.Parallel()
	req := config.RecommendationRecord{Count: 1, Service: spCapacitySlug}

	t.Run("unreadable stored details is 409 not 500", func(t *testing.T) {
		stored := spStored(`{"plan_type":"Compute"}`)
		_, err := priceFromStored(&req, &stored, 0, 50)
		ce, ok := IsClientError(err)
		require.True(t, ok, "got %v", err)
		assert.Equal(t, 409, ce.code)
		_, err = priceFromStored(&req, &stored, 0, 100)
		require.NoError(t, err, "100% never decodes the hourly")
	})

	t.Run("dash-free slug the sizer cannot scale keeps the 100 percent only refusal", func(t *testing.T) {
		stored := spStored(spDetails)
		stored.Service = "savingsplans-compute"
		r := req
		r.Service = "savingsplans-compute"
		_, err := priceFromStored(&r, &stored, 0, 50)
		ce, ok := IsClientError(err)
		require.True(t, ok, "got %v", err)
		assert.Equal(t, 400, ce.code)
		assert.Contains(t, ce.Error(), "100% capacity only")
		out, err := priceFromStored(&r, &stored, 0, 100)
		require.NoError(t, err)
		assert.Equal(t, 5000.0, out.UpfrontCost)
	})

	t.Run("non-SP below 100 still scales by count", func(t *testing.T) {
		stored := config.RecommendationRecord{Service: "ec2", Count: 4, UpfrontCost: 1000, Details: json.RawMessage(`{"platform":"Linux/UNIX"}`)}
		r := config.RecommendationRecord{Count: 2, Service: "ec2"}
		out, err := priceFromStored(&r, &stored, 0, 50)
		require.NoError(t, err)
		assert.Equal(t, 500.0, out.UpfrontCost)
		assert.Equal(t, `{"platform":"Linux/UNIX"}`, string(out.Details))
	})
}

func TestPurchaseIdempotencyKey_SavingsPlanScalingUnchanged(t *testing.T) {
	t.Parallel()
	stored := spStored(spDetails)
	req := config.RecommendationRecord{Count: 1, Service: spCapacitySlug}
	scaled, err := priceFromStored(&req, &stored, 0, 50)
	require.NoError(t, err)
	// The key covers the tuple and capacity, never the hourly: the scaled rec
	// hashes like the unscaled one, and 50% differs from 25%.
	assert.Equal(t, mustPurchaseIdempotencyKey("u", []config.RecommendationRecord{stored}, 50),
		mustPurchaseIdempotencyKey("u", []config.RecommendationRecord{scaled}, 50))
	assert.NotEqual(t, mustPurchaseIdempotencyKey("u", []config.RecommendationRecord{scaled}, 50),
		mustPurchaseIdempotencyKey("u", []config.RecommendationRecord{scaled}, 25))
}

// The approval email renders the persisted (sized) rec, never the stored
// recommendation's original figures (#1959): what the approver reads is what
// will be bought. The email has no hourly column; upfront, savings and count
// are its sizing-dependent figures.
func TestExecutePurchase_SavingsPlanApprovalEmailShowsSizedFigures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		line     string
		upfront  float64
		savings  float64
		capacity int
	}{
		{"half capacity", "Est. Savings: $400.00/month", 2500, 400, 50},
		{"full capacity unchanged", "Est. Savings: $800.00/month", 5000, 800, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := &recordingEmailNotifier{}
			saved, err := runSPExecuteWith(t, n, 0, spReqJSON(tc.capacity, ""), spStored(spDetails))
			require.NoError(t, err)
			require.NotNil(t, saved)
			require.Len(t, n.captured.Recommendations, 1)
			assert.Equal(t, 1, n.captured.Recommendations[0].Count)
			assert.Equal(t, tc.savings, n.captured.Recommendations[0].MonthlySavings)
			assert.Equal(t, tc.upfront, n.captured.TotalUpfrontCost)
			body, err := email.RenderPurchaseApprovalRequestEmail(n.captured)
			require.NoError(t, err)
			assert.Contains(t, body, tc.line)
		})
	}
}
