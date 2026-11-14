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
	t.Parallel()
	unattributed := dlRow(dlID2, nil, drRegion, "unknown", 3)
	tests := []struct {
		name   string
		id     string
		caller drCaller
		want   int // 0 = allowed
	}{
		{name: "execute:ri-exchange, unrestricted", caller: drCaller{perms: riExecute}, id: dlID1, want: 0},
		{name: "view-only user", caller: drCaller{perms: []auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePurchases}}}, id: dlID1, want: 403},
		{name: "admin API key has no identity to stamp", caller: drCaller{adminKey: true}, id: dlID1, want: 403},
		{name: "row account inside the session's accounts", caller: drCaller{perms: riExecute, accounts: []string{dlAcctA}}, id: dlID1, want: 0},
		{name: "row account outside the session's accounts reads as missing", caller: drCaller{perms: riExecute, accounts: []string{dlAcctB}}, id: dlID1, want: 404},
		{name: "unattributed row needs an unrestricted session", caller: drCaller{perms: riExecute, accounts: []string{dlAcctA}}, id: dlID2, want: 404},
		{name: "unattributed row, unrestricted session", caller: drCaller{perms: riExecute}, id: dlID2, want: 0},
		{name: "unknown id", caller: drCaller{perms: riExecute}, id: dlID3, want: 404},
		{name: "id that is not a uuid", caller: drCaller{perms: riExecute}, id: "not-a-uuid", want: 400},
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
	t.Parallel()
	grant := func(c auth.PermissionConstraints) []auth.Permission {
		return []auth.Permission{{Action: auth.ActionExecute, Resource: "ri-exchange", Constraints: &c}}
	}
	unattributed := dlRow(dlID2, nil, drRegion, "unknown", 3)
	tests := []struct {
		name  string
		id    string
		perms []auth.Permission
		want  int // 0 = allowed
	}{
		{name: "grant for the row's region", perms: grant(auth.PermissionConstraints{Regions: []string{drRegion}}), id: dlID1, want: 0},
		{name: "grant for another region", perms: grant(auth.PermissionConstraints{Regions: []string{"us-east-1"}}), id: dlID1, want: 403},
		{name: "grant for the row's account", perms: grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctA}}), id: dlID1, want: 0},
		{name: "grant for another account", perms: grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctB}}), id: dlID1, want: 403},
		{name: "grant for the deployment's account only", perms: grant(auth.PermissionConstraints{AccountIDs: []string{"33333333-3333-4333-8333-333333333333"}}), id: dlID1, want: 403},
		{name: "account-constrained grant never covers an unattributed row", perms: grant(auth.PermissionConstraints{AccountIDs: []string{dlAcctA}}), id: dlID2, want: 403},
		{name: "cap below the row's max_payment_due (100)", perms: riCapped(99.99), id: dlID1, want: 403},
		{name: "cap equal to it", perms: riCapped(100), id: dlID1, want: 0},
		{name: "cap above it", perms: riCapped(5000), id: dlID1, want: 0},
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
