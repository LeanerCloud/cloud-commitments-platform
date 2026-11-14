//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const settleBody = `{"reason":"checked the subscription: nothing was bought","acknowledge_unknown_outcome":true}`

var purchaserPerms = []auth.Permission{
	{Action: auth.ActionAdmin, Resource: auth.ResourceAll},
	{Action: auth.ActionExecute, Resource: auth.ResourcePurchases},
}

func (f *createConcurrencyFixture) settledAt(ctx context.Context, t *testing.T, id string) *time.Time {
	t.Helper()
	var at *time.Time
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT outcome_settled_at FROM purchase_executions WHERE execution_id = $1`, id).Scan(&at))
	return at
}

// The issue #999 scenario through the HTTP surface: a failed row with an
// unknown outcome makes the managed delete 409 until a purchaser settles it.
func TestSettleUnblocksManagedPlanDeleteOverHTTP(t *testing.T) {
	ctx := context.Background()
	f := newCreateConcurrencyFixture(ctx, t)
	setPlanManaged(ctx, t, f, true)
	id := f.saveRow(ctx, t, "failed", false, time.Now(), true)
	requireLiveExecutionsRefusal(t, f.deletePlan(ctx, f.handler))

	h := limitedHandler(f, purchaserPerms, nil)
	h.auth.(*MockAuthService).On("ValidateCSRFToken", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	req := managedGuardRequest(settleBody)
	req.Headers["X-CSRF-Token"] = "test-csrf"
	req.RequestContext.HTTP.Method = "POST"
	req.RequestContext.HTTP.Path = "/api/purchases/" + id + "/settle"
	resp, err := h.HandleRequest(ctx, req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, resp.Body)
	var got executionSettleResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Body), &got))
	require.Equal(t, id, got.ExecutionID)
	require.Equal(t, "failed", got.Status)
	require.Equal(t, "00000000-0000-0000-0000-000000000001", got.SettledBy)
	require.Contains(t, got.Settlement, `reason="checked the subscription: nothing was bought"`)
	require.False(t, got.Replayed)

	require.NoError(t, f.deletePlan(ctx, f.handler))
	row, err := f.store.GetExecutionByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "failed", row.Status)
	require.NotNil(t, f.settledAt(ctx, t, id))
}

func TestSettleAuthorizationAndValidation(t *testing.T) {
	ctx := context.Background()
	f := newCreateConcurrencyFixture(ctx, t)
	setPlanManaged(ctx, t, f, true)
	id := f.saveRow(ctx, t, "failed", false, time.Now(), true)
	completed := f.saveRow(ctx, t, "completed", false, time.Now(), true)
	var acct string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT account_id::text FROM plan_accounts WHERE plan_id = $1`, f.planID).Scan(&acct))
	constrained := func(c auth.PermissionConstraints) []auth.Permission {
		return []auth.Permission{{Action: auth.ActionExecute, Resource: auth.ResourcePurchases, Constraints: &c}}
	}
	cases := []struct {
		name   string
		id     string
		body   string
		perms  []auth.Permission
		scoped []string
		status int
	}{
		{name: "admin without execute", perms: []auth.Permission{{Action: auth.ActionAdmin, Resource: auth.ResourceAll}}, scoped: nil, id: id, body: settleBody, status: http.StatusForbidden},
		{name: "out of account scope", perms: purchaserPerms, scoped: []string{"other-account"}, id: id, body: settleBody, status: http.StatusNotFound},
		{name: "execute constrained to another account", perms: constrained(auth.PermissionConstraints{AccountIDs: []string{"other-account"}}), scoped: nil, id: id, body: settleBody, status: http.StatusForbidden},
		{name: "execute constrained to another provider", perms: constrained(auth.PermissionConstraints{AccountIDs: []string{acct}, Providers: []string{"aws"}}), scoped: nil, id: id, body: settleBody, status: http.StatusForbidden},
		{name: "no acknowledgement", perms: purchaserPerms, scoped: nil, id: id, body: `{"reason":"checked the subscription"}`, status: http.StatusBadRequest},
		{name: "short reason", perms: purchaserPerms, scoped: nil, id: id, body: `{"reason":"ok","acknowledge_unknown_outcome":true}`, status: http.StatusBadRequest},
		{name: "unknown field", perms: purchaserPerms, scoped: nil, id: id, body: `{"reason":"checked the subscription","acknowledge_unknown_outcome":true,"force":true}`, status: http.StatusBadRequest},
		{name: "completed row", perms: purchaserPerms, scoped: nil, id: completed, body: settleBody, status: http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := limitedHandler(f, tc.perms, tc.scoped)
			_, err := h.settleExecution(ctx, managedGuardRequest(tc.body), tc.id)
			var ce *clientError
			if errors.As(err, &ce) {
				require.Equal(t, tc.status, ce.code, ce.message)
			} else {
				require.True(t, IsNotFoundError(err) && tc.status == http.StatusNotFound, "got %v", err)
			}
			require.Nil(t, f.settledAt(ctx, t, tc.id), "a refused settle writes nothing")
		})
	}

	t.Run("constrained to the plan's account", func(t *testing.T) {
		h := limitedHandler(f, constrained(auth.PermissionConstraints{AccountIDs: []string{acct}}), nil)
		_, err := h.settleExecution(ctx, managedGuardRequest(settleBody), id)
		require.NoError(t, err)
	})
}
