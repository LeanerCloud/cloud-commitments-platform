package api

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Issue #1930: a fan-out root and its account rows each carry their own retry
// link, so the family guard refuses the second kind once the first was retried.

const (
	retryFamilyPlanID  = "33333333-3333-4444-5555-666666666601"
	retryFamilyAccount = "44444444-3333-4444-5555-666666666602"
)

// retryFamilyRow is a proven-unsent EC2 row of a plan: a root, or the account
// row of retryFamilyAccount.
func retryFamilyRow(asAccountRow bool) *config.PurchaseExecution {
	row := unsentRow("aws", "ec2")
	row.PlanID = retryFamilyPlanID
	row.IdempotencyKey = redriveLineageKey
	if asAccountRow {
		account := retryFamilyAccount
		row.CloudAccountID = &account
		row.IdempotencyKey = redriveLineageKey + ":" + account
	}
	return row
}

// expectFamilyLock registers the lock call and records its position in order.
func expectFamilyLock(store *MockConfigStore, order *[]string, err error) {
	store.On("LockRetryFamilyTx", mock.Anything, mock.Anything, redriveLineageKey).
		Run(func(mock.Arguments) { *order = append(*order, "lock") }).Return(err).Once()
}

func expectFamilyClaim(store *MockConfigStore, order *[]string, row *config.PurchaseExecution, claimed bool, err error) {
	store.On("FamilyRetryClaimedTx", mock.Anything, mock.Anything, redriveLineageKey, retryFamilyPlanID, row.ExecutionID, row.CloudAccountID == nil).
		Run(func(mock.Arguments) { *order = append(*order, "claim") }).Return(claimed, err).Once()
}

func TestRetryFamilyGuard_RefusesWhenCounterpartKindWasRetried(t *testing.T) {
	for _, tc := range []struct {
		name         string
		accountRow   bool
		wantRetried  string
		wantNotInMsg string
	}{
		{"root after an account row", false, "account rows", "root was"},
		{"account row after the root", true, "root", "account rows was"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failed := retryFamilyRow(tc.accountRow)
			handler, store := retryWithLinkMocks(t, failed)
			var order []string
			expectFamilyLock(store, &order, nil)
			expectFamilyClaim(store, &order, failed, true, nil)

			result, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

			assert.Nil(t, result)
			ce, ok := IsClientError(err)
			require.True(t, ok, "want a client error, got %v", err)
			assert.Equal(t, 409, ce.code)
			assert.Contains(t, ce.message, "whose "+tc.wantRetried+" was already retried")
			assert.NotContains(t, ce.message, tc.wantNotInMsg)
			assert.NotContains(t, ce.message, "was already retried or settled", "must be distinguishable from the row-level refusal")
			assert.Equal(t, map[string]any{"retry_family_claimed": true}, ce.details)
			assert.Equal(t, []string{"lock", "claim"}, order)
			store.AssertNotCalled(t, "SavePurchaseExecution", mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "CreateSuppressionTx", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestRetryFamilyGuard_UnclaimedFamilyProceedsLockBeforeClaimBeforeLink(t *testing.T) {
	for _, accountRow := range []bool{false, true} {
		failed := retryFamilyRow(accountRow)
		handler, store := retryWithLinkMocks(t, failed)
		var order []string
		expectFamilyLock(store, &order, nil)
		expectFamilyClaim(store, &order, failed, false, nil)
		store.On("LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).
			Run(func(mock.Arguments) { order = append(order, "link") }).Return(true, nil).Once()

		_, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

		require.NoError(t, err)
		assert.Equal(t, []string{"lock", "claim", "link"}, order, "accountRow=%v", accountRow)
	}
}

func TestRetryFamilyGuard_SkippedWhereItCannotDoubleBuy(t *testing.T) {
	rdsRow := retryFamilyRow(false)
	rdsRow.Recommendations[0].Service = "rds"
	noPlan := retryFamilyRow(false)
	noPlan.PlanID = ""
	for name, failed := range map[string]*config.PurchaseExecution{"dedupe-capable provider": rdsRow, "no plan": noPlan} {
		t.Run(name, func(t *testing.T) {
			handler, store := retryWithLinkMocks(t, failed)
			if failed.Recommendations[0].Service == "rds" {
				store.On("LinkRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil).Once()
			} else {
				store.On("LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil).Once()
			}

			_, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

			require.NoError(t, err)
			store.AssertNotCalled(t, "LockRetryFamilyTx", mock.Anything, mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "FamilyRetryClaimedTx", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// A failed lock or claim query is not a 409: nothing is linked and the error
// surfaces, so the guard fails closed.
func TestRetryFamilyGuard_StoreErrorsFailClosed(t *testing.T) {
	boom := errors.New("connection reset")
	for _, lockFails := range []bool{true, false} {
		failed := retryFamilyRow(false)
		handler, store := retryWithLinkMocks(t, failed)
		var order []string
		if lockFails {
			expectFamilyLock(store, &order, boom)
		} else {
			expectFamilyLock(store, &order, nil)
			expectFamilyClaim(store, &order, failed, false, boom)
		}

		_, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

		require.Error(t, err, "lockFails=%v", lockFails)
		_, isClient := IsClientError(err)
		assert.False(t, isClient, "a store error must not read as a 409")
		assert.ErrorIs(t, err, boom)
		store.AssertNotCalled(t, "LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	}
}

// A legacy row without a key is its own family: its account rows carry
// "<execution_id>:<account>", so the lock and claim use the execution ID.
func TestRetryFamilyKey(t *testing.T) {
	for name, tc := range map[string]struct {
		key, want string
	}{
		"root key":         {"k1", "k1"},
		"account row key":  {"k1:acct", "k1"},
		"legacy no key":    {"", redriveUnsentExecID},
		"legacy child key": {redriveUnsentExecID + ":acct", redriveUnsentExecID},
	} {
		t.Run(name, func(t *testing.T) {
			row := unsentRow("aws", "ec2")
			row.IdempotencyKey = tc.key
			assert.Equal(t, tc.want, retryFamilyKey(row))
		})
	}
}
