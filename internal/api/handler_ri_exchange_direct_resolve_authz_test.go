package api

import (
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveRIExchangeDirect_Authorization(t *testing.T) {
	unattributed := dlRow(dlID2, nil, drRegion, "unknown", 3)
	tests := []struct {
		name   string
		caller drCaller
		id     string
		want   int // 0 = allowed
	}{
		{"execute:ri-exchange, unrestricted", drCaller{perms: riExecute}, dlID1, 0},
		{"view-only user", drCaller{perms: []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePurchases}}}, dlID1, 403},
		{"admin API key has no identity to stamp", drCaller{adminKey: true}, dlID1, 403},
		{"row account inside the session's accounts", drCaller{perms: riExecute, accounts: []string{dlAcctA}}, dlID1, 0},
		{"row account outside the session's accounts reads as missing", drCaller{perms: riExecute, accounts: []string{dlAcctB}}, dlID1, 404},
		{"unattributed row needs an unrestricted session", drCaller{perms: riExecute, accounts: []string{dlAcctA}}, dlID2, 404},
		{"unattributed row, unrestricted session", drCaller{perms: riExecute}, dlID2, 0},
		{"unknown id", drCaller{perms: riExecute}, dlID3, 404},
		{"id that is not a uuid", drCaller{perms: riExecute}, "not-a-uuid", 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := drStore(dlRow(dlID1, dlPtr(dlAcctA), drRegion, "unknown", 1), unattributed)
			_, err := tc.caller.call(store, tc.id, drFailedBody)
			if tc.want == 0 {
				require.NoError(t, err)
				assert.Equal(t, 1, store.called)
				return
			}
			assert.Equal(t, 0, store.called, "a refused caller must not reach the store")
			if tc.want == 404 {
				require.ErrorIs(t, err, errNotFound)
				return
			}
			assert.Equal(t, tc.want, clientCode(t, err))
		})
	}
}

// The grant is judged against the ROW's account, region and payment cap. The
// deployment's own account is a different one in every case.
func TestResolveRIExchangeDirect_ConstraintsUseTheRowsAccountAndRegion(t *testing.T) {
	grant := func(c auth.PermissionConstraints) []auth.Permission {
		return []auth.Permission{{Action: auth.ActionExecute, Resource: "ri-exchange", Constraints: &c}}
	}
	unattributed := dlRow(dlID2, nil, drRegion, "unknown", 3)
	tests := []struct {
		name  string
		perms []auth.Permission
		id    string
		want  int // 0 = allowed
	}{
		{"grant for the row's region", grant(auth.PermissionConstraints{Regions: []string{drRegion}}), dlID1, 0},
		{"grant for another region", grant(auth.PermissionConstraints{Regions: []string{"us-east-1"}}), dlID1, 403},
		{"grant for the row's account", grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctA}}), dlID1, 0},
		{"grant for another account", grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctB}}), dlID1, 403},
		{"grant for the deployment's account only", grant(auth.PermissionConstraints{AccountIDs: []string{"33333333-3333-4333-8333-333333333333"}}), dlID1, 403},
		{"account-constrained grant never covers an unattributed row", grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctA}}), dlID2, 403},
		{"cap below the row's max_payment_due (100)", riCapped(99.99), dlID1, 403},
		{"cap equal to it", riCapped(100), dlID1, 0},
		{"cap above it", riCapped(5000), dlID1, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := drStore(dlRow(dlID1, dlPtr(dlAcctA), drRegion, "unknown", 1), unattributed)
			_, err := drCaller{perms: tc.perms}.call(store, tc.id, drFailedBody)
			if tc.want == 0 {
				require.NoError(t, err)
				assert.Equal(t, 1, store.called)
				return
			}
			assert.Equal(t, tc.want, clientCode(t, err))
			assert.Zero(t, store.called, "a refused caller must not reach the store")
		})
	}
}

func TestResolveRIExchangeDirect_CappedGrantRefusesUnreadableAmount(t *testing.T) {
	for _, due := range []string{"", "abc", "-1", "NaN", "+Inf"} {
		t.Run(due, func(t *testing.T) {
			row := dlRow(dlID1, dlPtr(dlAcctA), drRegion, "unknown", 1)
			row.MaxPaymentDue = due
			store := drStore(row)
			_, err := drCaller{perms: riCapped(5000)}.call(store, dlID1, drFailedBody)
			require.Error(t, err)
			assert.Zero(t, store.called)
		})
	}
}

// An uncapped grant never reads the amount, so an unreadable one passes.
func TestResolveRIExchangeDirect_UncappedGrantIgnoresTheAmount(t *testing.T) {
	row := dlRow(dlID1, dlPtr(dlAcctA), drRegion, "unknown", 1)
	row.MaxPaymentDue = "abc"
	store := drStore(row)
	_, err := drCaller{perms: riExecute}.call(store, dlID1, drFailedBody)
	require.NoError(t, err)
	assert.Equal(t, 1, store.called)
}

// No refusal or answer for one row may carry another row's id, and an
// out-of-scope row answers exactly like a missing one.
func TestResolveRIExchangeDirect_ResponsesNameOnlyTheRequestedRow(t *testing.T) {
	var seen []string
	collect := func(out any, err error) {
		if err != nil {
			seen = append(seen, err.Error())
			return
		}
		seen = append(seen, fmt.Sprintf("%+v", out))
	}
	for _, err := range []error{
		nil,
		fmt.Errorf("%w: the exchange is already completed", config.ErrRIExchangeResolveConflict),
		fmt.Errorf("%w: checked_at is in the future", config.ErrRIExchangeResolveInvalid),
		config.ErrRIExchangeResolveNotFound,
	} {
		store := drStore()
		store.err = err
		collect(drCaller{perms: riExecute}.call(store, dlID1, drFailedBody))
	}
	collect(drCaller{perms: riExecute, accounts: []string{dlAcctB}}.call(drStore(), dlID1, drFailedBody))
	collect(drCaller{perms: riExecute}.call(drStore(), dlID3, drFailedBody))
	collect(drCaller{perms: riCapped(1)}.call(drStore(), dlID1, drFailedBody))
	collect(drCaller{perms: riExecute}.call(drStore(), dlID1, `{"outcome":"nope"}`))
	for _, s := range seen {
		assert.NotContains(t, s, drOtherID)
	}

	_, outErr := drCaller{perms: riExecute, accounts: []string{dlAcctB}}.call(drStore(), dlID1, drFailedBody)
	_, missErr := drCaller{perms: riExecute, accounts: []string{dlAcctB}}.call(drStore(), dlID3, drFailedBody)
	assert.Equal(t, missErr, outErr, "out of scope must be indistinguishable from missing")
}
