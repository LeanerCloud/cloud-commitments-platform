package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	marketplaceHostKeyID   = "AKIAHOSTAMBIENT0000"
	marketplaceMemberKeyID = "AKIAMEMBERACCOUNT00"
	marketplaceHostAccount = "111111111111"
)

// accountKeyStore returns the member-account access key pair for every account.
type accountKeyStore struct{ MockCredentialStore }

func (*accountKeyStore) LoadRaw(_ context.Context, _, _ string) ([]byte, error) {
	return []byte(`{"access_key_id":"` + marketplaceMemberKeyID + `","secret_access_key":"member-secret"}`), nil
}

// hostSTSTransport answers every STS call as the host account.
type hostSTSTransport struct{}

func (hostSTSTransport) Do(*http.Request) (*http.Response, error) {
	const body = `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult>` +
		`<Arn>arn:aws:iam::` + marketplaceHostAccount + `:role/host</Arn><UserId>U</UserId><Account>` + marketplaceHostAccount +
		`</Account></GetCallerIdentityResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></GetCallerIdentityResponse>`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

// marketplaceAccountHandler wires a marketplace handler whose ambient AWS
// credentials belong to the host account. The returned pointer receives the
// config the EC2 client was built from.
func marketplaceAccountHandler(t *testing.T, account *config.CloudAccount, row *config.PurchaseHistoryRecord) (*Handler, *aws.Config) {
	t.Helper()
	cfgStore := &MockConfigStore{}
	authSvc := &MockAuthService{}
	adminSession(authSvc)
	cfgStore.On("GetPurchaseHistoryByPurchaseID", mock.Anything, validMarketplacePurchaseID).Return(row, nil)
	cfgStore.On("GetCloudAccount", mock.Anything, "acct-1").Return(account, nil)
	cfgStore.On("ClaimMarketplaceListingSlot", mock.Anything, validMarketplacePurchaseID).Return(true, nil).Maybe()
	cfgStore.On("UpdatePurchaseHistoryListing", mock.Anything, validMarketplacePurchaseID, mock.Anything, mock.Anything).Return(nil).Maybe()

	var built aws.Config
	h := &Handler{
		config:    cfgStore,
		credStore: &accountKeyStore{},
		auth:      authSvc,
		marketplaceEC2Factory: func(cfg aws.Config) marketplaceEC2Client {
			built = cfg
			return &stubMarketplaceEC2{}
		},
	}
	h.awsCfgOnce.Do(func() {
		h.awsCfg = aws.Config{
			Region:      "us-east-1",
			Credentials: awscreds.NewStaticCredentialsProvider(marketplaceHostKeyID, "host-secret", ""),
			HTTPClient:  hostSTSTransport{},
		}
	})
	return h, &built
}

func memberAccount() *config.CloudAccount {
	return &config.CloudAccount{ID: "acct-1", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"}
}

func signingKeyID(t *testing.T, cfg aws.Config) string {
	t.Helper()
	require.NotNil(t, cfg.Credentials, "EC2 client was built without credentials")
	creds, err := cfg.Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	return creds.AccessKeyID
}

func TestMarketplaceList_UsesPurchaseAccountCredentials(t *testing.T) {
	h, built := marketplaceAccountHandler(t, memberAccount(), standardRow())

	_, err := h.marketplaceList(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	require.NoError(t, err)
	assert.Equal(t, marketplaceMemberKeyID, signingKeyID(t, *built), "listing must be issued with the purchase account's credentials, not the host's")
	assert.Equal(t, "us-east-1", built.Region)
}

func TestMarketplaceCancel_UsesPurchaseAccountCredentials(t *testing.T) {
	row := standardRow()
	row.ListingState = config.ListingStateActive
	row.ListingID = "ril-cancel"
	h, built := marketplaceAccountHandler(t, memberAccount(), row)

	_, err := h.marketplaceCancel(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	require.NoError(t, err)
	assert.Equal(t, marketplaceMemberKeyID, signingKeyID(t, *built), "cancel must be issued with the purchase account's credentials, not the host's")
}

func TestMarketplaceList_RoleARNAccountWithoutRoleRefusedWhenNotHost(t *testing.T) {
	account := &config.CloudAccount{ID: "acct-1", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "role_arn"}
	h, built := marketplaceAccountHandler(t, account, standardRow())

	_, err := h.marketplaceList(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a client error, got %v", err)
	assert.Equal(t, 400, ce.code)
	assert.Equal(t, credentials.ErrNotHostAccount.Error(), ce.message)
	assert.NotContains(t, ce.message, marketplaceHostAccount)
	assert.Nil(t, built.Credentials, "no EC2 client may be built with the host's credentials for a non-host account")
}

func TestMarketplaceList_RoleARNAccountWithoutRoleAllowedForHost(t *testing.T) {
	account := &config.CloudAccount{ID: "acct-1", Provider: "aws", ExternalID: marketplaceHostAccount, AWSAuthMode: "role_arn"}
	h, built := marketplaceAccountHandler(t, account, standardRow())

	_, err := h.marketplaceList(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	require.NoError(t, err)
	assert.Equal(t, marketplaceHostKeyID, signingKeyID(t, *built))
}
