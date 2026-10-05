//go:build integration

package api

import (
	"context"
	"errors"
	"testing"

	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/migrations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// failFirstListingWriteStore fails only the first UpdatePurchaseHistoryListing,
// simulating a transient DB error right after AWS created the listing.
type failFirstListingWriteStore struct {
	config.StoreInterface
	failed bool
}

func (s *failFirstListingWriteStore) UpdatePurchaseHistoryListing(ctx context.Context, purchaseID, listingID, listingState string) error {
	if !s.failed {
		s.failed = true
		return errors.New("transient db failure")
	}
	return s.StoreInterface.UpdatePurchaseHistoryListing(ctx, purchaseID, listingID, listingState)
}

func TestMarketplaceListCancelFailureKeepsListingRecorded(t *testing.T) {
	ctx := t.Context()
	pg, err := testhelpers.SetupPostgresContainer(ctx, t)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Cleanup(context.Background())) })
	require.NoError(t, migrations.RunMigrations(ctx, pg.DB.Pool(), "../database/postgres/migrations", "", ""))
	store := config.NewPostgresStore(pg.DB)

	account := uuid.NewString()
	require.NoError(t, store.CreateCloudAccount(ctx, &config.CloudAccount{ID: account, Name: account, Provider: "aws", ExternalID: "111111111111", Enabled: true}))
	row := standardRow()
	row.PurchaseID, row.CloudAccountID, row.Provider, row.Service = uuid.NewString(), &account, "aws", "ec2"
	require.NoError(t, store.SavePurchaseHistory(ctx, row))

	authSvc := &MockAuthService{}
	authSvc.On("ValidateSession", mock.Anything, "test-token").Return(&Session{UserID: "admin", Email: "admin@test.com"}, nil)
	authSvc.On("HasPermissionAPI", mock.Anything, "admin", auth.ActionSellAny, auth.ResourcePurchases).Return(true, nil)
	authSvc.On("GetAllowedAccountsAPI", mock.Anything, "admin").Return([]string{"*"}, nil)
	authSvc.On("HasPermissionForConstraintsAPI", mock.Anything, "admin", auth.ActionSellAny, auth.ResourcePurchases, mock.Anything).Return(true, nil)

	cancelErr := errors.New("RequestLimitExceeded")
	ec2 := &stubMarketplaceEC2{cancelFn: func(_ context.Context, id string) (ec2svc.MarketplaceListingResult, error) {
		if cancelErr != nil {
			return ec2svc.MarketplaceListingResult{}, cancelErr
		}
		return ec2svc.MarketplaceListingResult{ListingID: id, State: config.ListingStateCancelled}, nil
	}}
	h := &Handler{config: &failFirstListingWriteStore{StoreInterface: store}, auth: authSvc,
		marketplaceEC2Factory: func(_ aws.Config) marketplaceEC2Client { return ec2 }}
	h.awsCfgOnce.Do(func() { h.awsCfg = aws.Config{Region: "us-east-1"} })

	_, err = h.marketplaceList(ctx, marketplaceReq(), row.PurchaseID)
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, 502, ce.code)
	assert.NotContains(t, ce.message, "rolled back")
	assert.Contains(t, ce.message, "RequestLimitExceeded")

	stored, err := store.GetPurchaseHistoryByPurchaseID(ctx, row.PurchaseID)
	require.NoError(t, err)
	assert.Equal(t, "ril-default", stored.ListingID)
	assert.Equal(t, config.ListingStateActive, stored.ListingState)

	_, err = h.marketplaceList(ctx, marketplaceReq(), row.PurchaseID)
	require.Error(t, err, "a second listing must not be created while the first is live")
	assert.Equal(t, 1, ec2.createCallCount)

	cancelErr = nil
	_, err = h.marketplaceCancel(ctx, marketplaceReq(), row.PurchaseID)
	require.NoError(t, err)
	stored, err = store.GetPurchaseHistoryByPurchaseID(ctx, row.PurchaseID)
	require.NoError(t, err)
	assert.Equal(t, "ril-default", stored.ListingID)
	assert.Equal(t, config.ListingStateCancelled, stored.ListingState)
}
