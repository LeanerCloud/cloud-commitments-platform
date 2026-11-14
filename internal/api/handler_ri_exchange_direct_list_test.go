package api

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Handler tests for GET /api/ri-exchange/direct-executions (#1836). The store
// is an in-memory fixture that applies the allowed set and the keyset the way
// the SQL does (covered against real PostgreSQL in internal/config); these
// tests pin which (account, region) pairs the handler hands it.

type directListStore struct {
	*MockConfigStore
	rows       []config.RIExchangeDirectRow
	lastFilter config.RIExchangeDirectListFilter
	listCalls  int
	pairCalls  int
}

func (s *directListStore) ListUnresolvedRIExchangeDirectAccountRegions(context.Context) ([]config.RIExchangeDirectAccountRegion, error) {
	s.pairCalls++
	seen := map[string]bool{}
	var out []config.RIExchangeDirectAccountRegion
	for _, r := range s.rows {
		k := pairKey(r.CloudAccountID, r.Region)
		if !seen[k] {
			seen[k] = true
			out = append(out, config.RIExchangeDirectAccountRegion{CloudAccountID: r.CloudAccountID, Region: r.Region})
		}
	}
	return out, nil
}

func pairKey(account *string, region string) string {
	if account == nil {
		return "<nil>|" + region
	}
	return *account + "|" + region
}

func (s *directListStore) ListUnresolvedRIExchangeDirectExecutions(_ context.Context, f config.RIExchangeDirectListFilter) (*config.RIExchangeDirectListPage, error) {
	s.listCalls++
	s.lastFilter = f
	allowed := map[string]bool{}
	for _, p := range f.Allowed {
		allowed[pairKey(p.CloudAccountID, p.Region)] = true
	}
	var rows []config.RIExchangeDirectRow
	for _, r := range s.rows {
		if !allowed[pairKey(r.CloudAccountID, r.Region)] {
			continue
		}
		if f.After != nil && !r.CreatedAt.After(f.After.CreatedAt) && (!r.CreatedAt.Equal(f.After.CreatedAt) || r.ID <= f.After.ID) {
			continue
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.Before(rows[j].CreatedAt)
		}
		return rows[i].ID < rows[j].ID
	})
	page := &config.RIExchangeDirectListPage{}
	if f.Limit > 0 && len(rows) > f.Limit {
		rows = rows[:f.Limit]
		page.Next = &config.RIExchangeDirectCursor{CreatedAt: rows[f.Limit-1].CreatedAt, ID: rows[f.Limit-1].ID}
	}
	page.Rows = rows
	return page, nil
}

func (s *directListStore) GetRIExchangeDirectExecution(context.Context, string) (*config.RIExchangeDirectRow, error) {
	return nil, config.ErrRIExchangeResolveNotFound
}

func dlStore(rows ...config.RIExchangeDirectRow) *directListStore {
	s := &directListStore{MockConfigStore: new(MockConfigStore), rows: rows}
	s.ListCloudAccountsFn = func(context.Context, config.CloudAccountFilter) ([]config.CloudAccount, error) { return nil, nil }
	return s
}

// dlCall runs the handler as a session user holding perms, scoped to accounts
// (nil means unrestricted).
func dlCall(t *testing.T, store *directListStore, perms []auth.Permission, accounts []string, query map[string]string) (any, error) {
	t.Helper()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped(perms, accounts)
	h := &Handler{config: store, auth: mockAuth}
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}, QueryStringParameters: query}
	return h.listRIExchangeDirectExecutions(context.Background(), req)
}

func dlIDs(t *testing.T, out any) []string {
	t.Helper()
	resp, ok := out.(riExchangeDirectListResponse)
	require.True(t, ok)
	ids := make([]string, len(resp.Executions))
	for i, e := range resp.Executions {
		ids[i] = e.ID
	}
	return ids
}

func regionGrant(regions ...string) []auth.Permission {
	perms := make([]auth.Permission, 0, len(regions))
	for _, r := range regions {
		perms = append(perms, auth.Permission{Action: auth.ActionExecute, Resource: "ri-exchange",
			Constraints: &auth.PermissionConstraints{Regions: []string{r}}})
	}
	return perms
}

func TestListRIExchangeDirectExecutions_NeedsExecutePermission(t *testing.T) {
	t.Parallel()
	store := dlStore(dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1))
	_, err := dlCall(t, store, []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePurchases}}, nil, nil)
	assert.Equal(t, 403, clientCode(t, err))
	assert.Zero(t, store.pairCalls+store.listCalls, "a refused caller must not reach the store")
}

func TestListRIExchangeDirectExecutions_AccountScope(t *testing.T) {
	t.Parallel()
	store := dlStore(
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctB), "eu-west-1", "unknown", 2),
		dlRow(dlID3, nil, "eu-west-1", "submitted", 3),
	)
	t.Run("scoped session sees its account only, never an unattributed row", func(t *testing.T) {
		out, err := dlCall(t, store, riExecute, []string{dlAcctA}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID1}, dlIDs(t, out))
	})
	t.Run("unrestricted session sees every account and the unattributed row", func(t *testing.T) {
		out, err := dlCall(t, store, riExecute, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID1, dlID2, dlID3}, dlIDs(t, out))
	})
	t.Run("nothing visible is an empty list, not an error", func(t *testing.T) {
		out, err := dlCall(t, store, riExecute, []string{"33333333-3333-4333-8333-333333333333"}, nil)
		require.NoError(t, err)
		raw, _ := json.Marshal(out)
		assert.JSONEq(t, `{"executions":[],"next_cursor":""}`, string(raw))
	})
}

func TestListRIExchangeDirectExecutions_GrantRegions(t *testing.T) {
	t.Parallel()
	store := dlStore(
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctA), "us-east-1", "unknown", 2),
		dlRow(dlID3, dlPtr(dlAcctA), "ap-south-1", "unknown", 3),
	)
	t.Run("a grant restricted to one region", func(t *testing.T) {
		out, err := dlCall(t, store, regionGrant("us-east-1"), nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID2}, dlIDs(t, out))
	})
	t.Run("two grants are OR'd", func(t *testing.T) {
		out, err := dlCall(t, store, regionGrant("us-east-1", "eu-west-1"), nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID1, dlID2}, dlIDs(t, out))
	})
	t.Run("region spelling is not case sensitive", func(t *testing.T) {
		out, err := dlCall(t, store, regionGrant("EU-WEST-1"), nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID1}, dlIDs(t, out))
	})
	t.Run("an account-constrained grant hides other accounts' rows", func(t *testing.T) {
		other := dlStore(dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1), dlRow(dlID2, dlPtr(dlAcctB), "eu-west-1", "unknown", 2))
		perms := []auth.Permission{{Action: auth.ActionExecute, Resource: "ri-exchange",
			Constraints: &auth.PermissionConstraints{AccountIDs: []string{dlAcctB}}}}
		out, err := dlCall(t, other, perms, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID2}, dlIDs(t, out))
	})
}

// A user API key is judged on its own permissions, not its owner's.
func TestListRIExchangeDirectExecutions_UserAPIKeyIsNarrowerThanItsOwner(t *testing.T) {
	t.Parallel()
	store := dlStore(
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctA), "us-east-1", "unknown", 2),
	)
	mockAuth := new(MockAuthService)
	mockAuth.On("HasAPIKeyPermissionAPI", mock.Anything, "user-key", "execute", "ri-exchange").Return("u2", "key-1", true, nil)
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, "u2").Return([]string(nil), nil)
	inRegion := func(region string) any {
		return mock.MatchedBy(func(sets []auth.PermissionConstraints) bool {
			return len(sets) == 1 && len(sets[0].Regions) == 1 && sets[0].Regions[0] == region
		})
	}
	mockAuth.On("HasAPIKeyPermissionForConstraintsAPI", mock.Anything, "key-1", "u2", "execute", "ri-exchange", inRegion("us-east-1")).Return(true, nil)
	mockAuth.On("HasAPIKeyPermissionForConstraintsAPI", mock.Anything, "key-1", "u2", "execute", "ri-exchange", inRegion("eu-west-1")).Return(false, nil)
	h := &Handler{config: store, auth: mockAuth}
	ctx := contextWithPrincipal(context.Background(), &Principal{Kind: PrincipalUserAPIKey, UserID: "u2", APIKeyID: "key-1"})

	out, err := h.listRIExchangeDirectExecutions(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{"x-api-key": "user-key"}})

	require.NoError(t, err)
	assert.Equal(t, []string{dlID2}, dlIDs(t, out))
	mockAuth.AssertNotCalled(t, "HasPermissionForConstraintsAPI", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// Rows of a denied region sit between visible ones: every returned row and
// every cursor belongs to a visible pair, and paging is complete.
func TestListRIExchangeDirectExecutions_CursorNeverPointsAtADeniedRow(t *testing.T) {
	t.Parallel()
	store := dlStore(
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctA), "us-east-1", "unknown", 2),
		dlRow(dlID3, dlPtr(dlAcctA), "eu-west-1", "unknown", 3),
		dlRow(dlID4, dlPtr(dlAcctA), "us-east-1", "unknown", 4),
	)
	denied := map[string]bool{dlID2: true, dlID4: true}
	var got []string
	query := map[string]string{"limit": "1"}
	for page := 0; page < 5; page++ {
		out, err := dlCall(t, store, regionGrant("eu-west-1"), nil, query)
		require.NoError(t, err)
		got = append(got, dlIDs(t, out)...)
		next := out.(riExchangeDirectListResponse).NextCursor
		if next == "" {
			break
		}
		cur, err := decodeRIExchangeDirectCursor(next)
		require.NoError(t, err)
		assert.False(t, denied[cur.ID], "the cursor points at a row the caller cannot see")
		query = map[string]string{"limit": "1", "cursor": next}
	}
	assert.Equal(t, []string{dlID1, dlID3}, got)
}

func TestListRIExchangeDirectExecutions_NoStore503(t *testing.T) {
	t.Parallel()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.grantPermissions(riExecute)
	h := &Handler{config: new(MockConfigStore), auth: mockAuth}
	_, err := h.listRIExchangeDirectExecutions(context.Background(),
		&events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}})
	assert.Equal(t, 503, clientCode(t, err))
}

// A group may list an account by its AWS account number. The list must admit
// the row of the account that number names, not hide it because only the
// internal id was compared.
func TestListRIExchangeDirectExecutions_AllowedAccountsMayNameTheAWSAccountNumber(t *testing.T) {
	t.Parallel()
	rows := []config.RIExchangeDirectRow{
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctB), "eu-west-1", "unknown", 2),
	}
	accounts := []config.CloudAccount{
		{ID: dlAcctA, Provider: "aws", ExternalID: "111122223333"},
		{ID: dlAcctB, Provider: "aws", ExternalID: "999999999999"},
	}
	for name, tc := range map[string]struct {
		allowed []string
		want    []string
	}{
		"the account number":                {[]string{"111122223333"}, []string{dlID1}},
		"the internal id":                   {[]string{dlAcctA}, []string{dlID1}},
		"the other account's number":        {[]string{"999999999999"}, []string{dlID2}},
		"a number that is nobody's":         {[]string{"000000000000"}, nil},
		"both, one by number and one by id": {[]string{"111122223333", dlAcctB}, []string{dlID1, dlID2}},
	} {
		t.Run(name, func(t *testing.T) {
			store := dlStore(rows...)
			store.ListCloudAccountsFn = func(context.Context, config.CloudAccountFilter) ([]config.CloudAccount, error) { return accounts, nil }
			out, err := dlCall(t, store, riExecute, tc.allowed, nil)
			require.NoError(t, err)
			assert.ElementsMatch(t, tc.want, dlIDs(t, out))
		})
	}
}
