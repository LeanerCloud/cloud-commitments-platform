package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExecutePurchase_SingleAccount_AccountNotFound asserts that when a target
// account ID is known but GetCloudAccount returns (nil, nil) (the account does
// not exist), execution surfaces an error rather than silently falling back to
// ambient credentials and purchasing/stamping against the wrong account (#646).
func TestExecutePurchase_SingleAccount_AccountNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	acctID := "missing-acct"
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-acct-missing",
		PlanID:      "",
		Recommendations: []config.RecommendationRecord{
			{
				Provider:       "azure",
				Service:        "compute",
				ResourceType:   "Standard_B1ls",
				Region:         "eastus",
				Count:          1,
				Selected:       true,
				CloudAccountID: &acctID,
			},
		},
	}

	// GetCloudAccount returns no error but a nil account (not found).
	mockStore.On("GetCloudAccount", ctx, acctID).Return(nil, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credential resolution failed for account "+acctID)
	assert.Contains(t, err.Error(), "account not found")

	mockStore.AssertExpectations(t)
}

// TestSingleCloudAccountIDFromRecs covers the helper that derives a shared
// cloud_account_id from the SELECTED recommendations of a plan-less
// execution, and the #1902 ambiguous-scope errors it returns for the
// multi-account and mixed attributed/unattributed shapes.
func TestSingleCloudAccountIDFromRecs(t *testing.T) {
	aid1 := "acct-1"
	aid2 := "acct-2"
	aid3 := "acct-3"

	tests := []struct {
		want    *string
		name    string
		wantMsg string
		recs    []config.RecommendationRecord
		wantErr bool
	}{
		{
			name: "empty slice returns nil",
			recs: []config.RecommendationRecord{},
			want: nil,
		},
		{
			name: "all unattributed returns nil",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: nil},
				{Selected: true, CloudAccountID: nil},
			},
			want: nil,
		},
		{
			name: "single attributed rec returns it",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
			},
			want: &aid1,
		},
		{
			name: "all recs share one account",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid1},
			},
			want: &aid1,
		},
		{
			name: "unselected rec from another account is ignored",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: false, CloudAccountID: &aid2},
			},
			want: &aid1,
		},
		{
			name: "only unselected recs returns nil",
			recs: []config.RecommendationRecord{
				{Selected: false, CloudAccountID: &aid1},
			},
			want: nil,
		},
		{
			name: "two distinct accounts is an error",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid2},
			},
			wantErr: true,
			wantMsg: "2 cloud accounts (acct-1, acct-2)",
		},
		{
			name: "mixed attributed and unattributed is an error",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: nil},
				{Selected: true, CloudAccountID: &aid1},
			},
			wantErr: true,
			wantMsg: "carry no cloud_account_id",
		},
		{
			name: "three distinct accounts lists all three in order",
			recs: []config.RecommendationRecord{
				{Selected: true, CloudAccountID: &aid1},
				{Selected: true, CloudAccountID: &aid2},
				{Selected: true, CloudAccountID: &aid3},
			},
			wantErr: true,
			wantMsg: "(acct-1, acct-2, acct-3)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SingleCloudAccountIDFromRecs(tc.recs)
			if tc.wantErr {
				require.ErrorIs(t, err, errAmbiguousAccountScope)
				assert.Contains(t, err.Error(), tc.wantMsg)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tc.want == nil {
				assert.Nil(t, got)
			} else {
				require.NotNil(t, got)
				assert.Equal(t, *tc.want, *got)
			}
		})
	}
}
