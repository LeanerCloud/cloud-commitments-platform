package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Issue #1130: resuming a paused planned purchase puts it back on schedule, so
// it counts against MaxPurchaseAmount like a spend. The cap is on the TOTAL
// commitment (upfront plus recurring over the term), by the verb that
// authorized the resume. Rows are azure/compute/westeurope on "acct-1".

func capOf(amount float64) *auth.PermissionConstraints {
	return &auth.PermissionConstraints{MaxPurchaseAmount: amount}
}

// resumeCapRec is a one-year rec in the cancelRouteExec scope with the given costs.
func resumeCapRec(upfront, monthly float64, currency string) config.RecommendationRecord {
	acct := "acct-1"
	rec := config.RecommendationRecord{
		ID: "r1", Provider: "azure", Service: "compute", Region: "westeurope", CloudAccountID: &acct,
		Count: 1, UpfrontCost: upfront, Term: 1, CommitmentCurrency: currency,
	}
	if monthly != 0 {
		rec.MonthlyCost = &monthly
	}
	return rec
}

// noUpfrontRecs totals 1200: no upfront, 100/month over one year.
func noUpfrontRecs() []config.RecommendationRecord {
	return []config.RecommendationRecord{resumeCapRec(0, 100, "USD")}
}

type resumeCapCase struct {
	creator *string
	recs    []config.RecommendationRecord
	perms   []auth.Permission
	// totalUpfront is the stored TotalUpfrontCost, deliberately unrelated to recs.
	totalUpfront float64
}

// runResumeCap resumes a paused row as session u-1 with exactly perms; the
// pinned transition expects sawEmpty to match whether recs is empty.
func runResumeCap(t *testing.T, c resumeCapCase) (*StatusResponse, *MockConfigStore, error) {
	t.Helper()
	exec := cancelRouteExec("paused")
	exec.CreatedByUserID = c.creator
	exec.ScheduledDate = time.Now().Add(24 * time.Hour)
	exec.Recommendations = c.recs
	exec.TotalUpfrontCost = c.totalUpfront
	h, store := cancelRouteHandler(t, exec, c.perms...)
	resumed := *exec
	resumed.Status = "pending"
	store.On("TransitionExecutionStatusPinned", mock.Anything, cancelExecID, []string{"paused"}, "pending", mock.Anything, len(c.recs) == 0).
		Return(&resumed, nil).Maybe()
	resp, err := h.resumePlannedPurchase(context.Background(), deletePlannedReq(), cancelExecID)
	return resp, store, err
}

func requireResumeNotTransitioned(t *testing.T, store *MockConfigStore) {
	t.Helper()
	requireNotTransitioned(t, store)
	store.AssertNotCalled(t, "TransitionExecutionStatusPinned", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestResumeAmountCap_UpdateAnyChargesTotalCommitment(t *testing.T) {
	t.Parallel()
	other := pauseResumeOtherUser
	for _, tc := range []struct {
		name         string
		cap          float64
		totalUpfront float64
		recs         []config.RecommendationRecord
		denied       bool
	}{
		{"no-upfront recurring cost above the cap", 500, 0, noUpfrontRecs(), true},
		{"stored upfront below the cap does not hide the recs", 500, 1, noUpfrontRecs(), true},
		{"stored upfront above the cap does not price the recs", 500, 9999, []config.RecommendationRecord{resumeCapRec(100, 0, "USD")}, false},
		{"total equal to the cap", 1200, 0, noUpfrontRecs(), false},
		{"uncapped", 0, 0, noUpfrontRecs(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var c *auth.PermissionConstraints
			if tc.cap != 0 {
				c = capOf(tc.cap)
			}
			resp, store, err := runResumeCap(t, resumeCapCase{
				creator: &other, recs: tc.recs, totalUpfront: tc.totalUpfront,
				perms: []auth.Permission{revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, c)},
			})
			if tc.denied {
				requireRevokeForbidden(t, err, constraintDenied)
				assert.NotContains(t, err.Error(), "1200", "the refusal must not echo the commitment")
				requireResumeNotTransitioned(t, store)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "resumed", resp.Status)
		})
	}
}

func TestResumeAmountCap_CreatorOnUpdateIsCapped(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	run := func(cap float64) (*StatusResponse, *MockConfigStore, error) {
		var c *auth.PermissionConstraints
		if cap != 0 {
			c = capOf(cap)
		}
		return runResumeCap(t, resumeCapCase{creator: &me, recs: noUpfrontRecs(), perms: []auth.Permission{revokePerm(auth.ActionUpdate, c)}})
	}
	t.Run("above the cap is refused", func(t *testing.T) {
		_, store, err := run(500)
		requireRevokeForbidden(t, err, constraintDenied)
		requireResumeNotTransitioned(t, store)
	})
	t.Run("at the cap resumes", func(t *testing.T) {
		_, _, err := run(1200)
		require.NoError(t, err)
	})
	t.Run("uncapped resumes", func(t *testing.T) {
		_, _, err := run(0)
		require.NoError(t, err)
	})
}

// The creator fallback after an update-any scope denial must not bypass the cap
// on update.
func TestResumeAmountCap_CreatorFallbackStillCapped(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	wrongAccount := revokePerm(auth.ActionUpdateAny, &auth.PermissionConstraints{AccountIDs: []string{"other-acct"}})
	_, store, err := runResumeCap(t, resumeCapCase{creator: &me, recs: noUpfrontRecs(),
		perms: []auth.Permission{wrongAccount, revokePerm(auth.ActionUpdate, capOf(500))}})
	requireRevokeForbidden(t, err, constraintDenied)
	requireResumeNotTransitioned(t, store)
}

// An in-scope update-any holder is charged on update-any's cap even for their
// own row, as approve-any is.
func TestResumeAmountCap_InScopeUpdateAnyCreatorChargedOnUpdateAny(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	_, store, err := runResumeCap(t, resumeCapCase{creator: &me, recs: noUpfrontRecs(),
		perms: []auth.Permission{revokePerm(auth.ActionUpdateAny, capOf(500)), revokePerm(auth.ActionUpdate, nil)}})
	requireRevokeForbidden(t, err, constraintDenied)
	requireResumeNotTransitioned(t, store)
}

// A creator whose update-any is scoped elsewhere resumes their own uncapped row.
func TestResumeAmountCap_ScopedUpdateAnyCreatorUncappedUpdateResumes(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	wrongAccount := revokePerm(auth.ActionUpdateAny, &auth.PermissionConstraints{AccountIDs: []string{"acct-A"}})
	resp, _, err := runResumeCap(t, resumeCapCase{creator: &me, recs: noUpfrontRecs(),
		perms: []auth.Permission{wrongAccount, revokePerm(auth.ActionUpdate, nil)}})
	require.NoError(t, err)
	assert.Equal(t, "resumed", resp.Status)
}

// The update branch is checked on the row's scope, as approve-own is: an
// update grant scoped to another region does not cover the row.
func TestResumeAmountCap_UpdateBranchUsesRowScope(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	scoped := &auth.PermissionConstraints{Regions: []string{"eastus"}, MaxPurchaseAmount: 5000}
	_, store, err := runResumeCap(t, resumeCapCase{creator: &me, recs: noUpfrontRecs(),
		perms: []auth.Permission{revokePerm(auth.ActionUpdate, scoped)}})
	requireRevokeForbidden(t, err, constraintDenied)
	requireResumeNotTransitioned(t, store)
}

// An uncapped grant scoped elsewhere must not lift the cap of the grant that
// covers the row (the azure/westeurope row totals 1200), on either verb.
func TestResumeAmountCap_UncappedGrantElsewhereDoesNotLiftCap(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	other := pauseResumeOtherUser
	for _, tc := range []struct {
		name    string
		verb    string
		creator *string
	}{
		{"update, creator's own row", auth.ActionUpdate, &me},
		{"update-any, another user's row", auth.ActionUpdateAny, &other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capped := revokePerm(tc.verb, &auth.PermissionConstraints{Providers: []string{"azure"}, MaxPurchaseAmount: 500})
			elsewhere := revokePerm(tc.verb, &auth.PermissionConstraints{Regions: []string{"eastus"}})
			perms := []auth.Permission{capped, elsewhere}
			if tc.verb == auth.ActionUpdateAny {
				perms = append(perms, revokePerm(auth.ActionUpdate, nil)) // so the request passes the update gate
			}
			_, store, err := runResumeCap(t, resumeCapCase{creator: tc.creator, recs: noUpfrontRecs(), perms: perms})
			requireRevokeForbidden(t, err, constraintDenied)
			requireResumeNotTransitioned(t, store)
		})
	}
}

func TestResumeAmountCap_NonUSDUnderCap(t *testing.T) {
	t.Parallel()
	other := pauseResumeOtherUser
	me := revokeConstraintsUser
	eur := []config.RecommendationRecord{resumeCapRec(100, 0, "EUR")}
	mixed := []config.RecommendationRecord{resumeCapRec(100, 0, "USD"), resumeCapRec(100, 0, "EUR")}
	for _, branch := range []struct {
		name    string
		creator *string
		verb    string
	}{{"update-any", &other, auth.ActionUpdateAny}, {"update", &me, auth.ActionUpdate}} {
		perms := func(c *auth.PermissionConstraints) []auth.Permission {
			return []auth.Permission{revokePerm(branch.verb, c), revokePerm(auth.ActionUpdate, c)}
		}
		if branch.verb == auth.ActionUpdate {
			perms = func(c *auth.PermissionConstraints) []auth.Permission {
				return []auth.Permission{revokePerm(auth.ActionUpdate, c)}
			}
		}
		t.Run(branch.name+"/non-USD under a cap is refused", func(t *testing.T) {
			t.Parallel()
			_, store, err := runResumeCap(t, resumeCapCase{creator: branch.creator, recs: eur, perms: perms(capOf(5000))})
			requireRevokeForbidden(t, err, "USD spend cap")
			requireResumeNotTransitioned(t, store)
		})
		t.Run(branch.name+"/mixed USD and non-USD under a cap is refused", func(t *testing.T) {
			t.Parallel()
			_, store, err := runResumeCap(t, resumeCapCase{creator: branch.creator, recs: mixed, perms: perms(capOf(5000))})
			requireRevokeForbidden(t, err, "USD spend cap")
			requireResumeNotTransitioned(t, store)
		})
		t.Run(branch.name+"/non-USD uncapped resumes", func(t *testing.T) {
			t.Parallel()
			_, _, err := runResumeCap(t, resumeCapCase{creator: branch.creator, recs: eur, perms: perms(nil)})
			require.NoError(t, err)
		})
	}
}

// #1866: a legacy row with no CommitmentCurrency counts as non-USD. A paused row cannot
// be refreshed, so the refusal must not point a capped resumer at a refresh.
func TestResumeAmountCap_LegacyEmptyCurrencyRefusalSaysWhoCanResume(t *testing.T) {
	t.Parallel()
	me := revokeConstraintsUser
	legacy := []config.RecommendationRecord{resumeCapRec(100, 0, "")}
	perms := []auth.Permission{revokePerm(auth.ActionUpdate, capOf(5000))}

	_, store, err := runResumeCap(t, resumeCapCase{creator: &me, recs: legacy, perms: perms})

	requireRevokeForbidden(t, err, "USD spend cap")
	requireRevokeForbidden(t, err, "a stored or paused purchase cannot be refreshed")
	requireRevokeForbidden(t, err, "without a spend cap must act on it instead")
	requireRevokeForbidden(t, err, "unknown currency")
	requireResumeNotTransitioned(t, store)
}

// A row whose recs total zero is refused (400) as approve refuses it, on both branches.
func TestResumeAmountCap_ZeroCommitmentRefused(t *testing.T) {
	t.Parallel()
	other := pauseResumeOtherUser
	me := revokeConstraintsUser
	zero := []config.RecommendationRecord{resumeCapRec(0, 0, "USD")}
	for name, c := range map[string]resumeCapCase{
		"update-any": {creator: &other, recs: zero, perms: []auth.Permission{revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, capOf(500))}},
		"update":     {creator: &me, recs: zero, perms: []auth.Permission{revokePerm(auth.ActionUpdate, capOf(500))}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, store, err := runResumeCap(t, c)
			ce, ok := IsClientError(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, 400, ce.code)
			assert.Contains(t, ce.Error(), "zero total commitment")
			requireResumeNotTransitioned(t, store)
		})
	}
}

// Deliberate behavior change (issue #1130): the zero-commitment guard applies
// to every caller, uncapped ones included, exactly as approve applies it.
func TestResumeAmountCap_ZeroCommitmentRefusedEvenWhenUncapped(t *testing.T) {
	t.Parallel()
	other := pauseResumeOtherUser
	me := revokeConstraintsUser
	zero := []config.RecommendationRecord{resumeCapRec(0, 0, "USD")}
	for name, c := range map[string]resumeCapCase{
		"update-any": {creator: &other, recs: zero, perms: []auth.Permission{revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, nil)}},
		"update":     {creator: &me, recs: zero, perms: []auth.Permission{revokePerm(auth.ActionUpdate, nil)}},
		"admin":      {creator: &other, recs: zero, perms: []auth.Permission{{Action: auth.ActionAdmin, Resource: auth.ResourceAll}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, store, err := runResumeCap(t, c)
			ce, ok := IsClientError(err)
			require.True(t, ok, "got %v", err)
			assert.Equal(t, 400, ce.code)
			requireResumeNotTransitioned(t, store)
		})
	}
}

// With no recommendations there is nothing to price: the amount check is
// skipped and the transition is pinned to the empty-recs state.
func TestResumeAmountCap_EmptyRecsSkipCheckAndPinTheTransition(t *testing.T) {
	t.Parallel()
	other := pauseResumeOtherUser
	resp, store, err := runResumeCap(t, resumeCapCase{creator: &other, recs: []config.RecommendationRecord{},
		perms: []auth.Permission{revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, capOf(500))}})
	require.NoError(t, err)
	assert.Equal(t, "resumed", resp.Status)
	store.AssertCalled(t, "TransitionExecutionStatusPinned", mock.Anything, cancelExecID, []string{"paused"}, "pending", mock.Anything, true)
}

// Plan-step rows have no creator: update-any or admin only.
func TestResumeAmountCap_PlanStepRow(t *testing.T) {
	t.Parallel()
	recs := noUpfrontRecs()
	run := func(perms ...auth.Permission) (*StatusResponse, *MockConfigStore, error) {
		exec := cancelRouteExec("paused")
		exec.PlanID = "plan-1"
		exec.CreatedByUserID = nil
		exec.ScheduledDate = time.Now().Add(24 * time.Hour)
		exec.Recommendations = recs
		h, store := cancelRouteHandler(t, exec, perms...)
		resumed := *exec
		resumed.Status = "pending"
		store.On("TransitionExecutionStatusPinned", mock.Anything, cancelExecID, []string{"paused"}, "pending", mock.Anything, false).Return(&resumed, nil).Maybe()
		resp, err := h.resumePlannedPurchase(context.Background(), deletePlannedReq(), cancelExecID)
		return resp, store, err
	}
	t.Run("update alone is refused", func(t *testing.T) {
		t.Parallel()
		_, store, err := run(revokePerm(auth.ActionUpdate, nil))
		requireRevokeForbidden(t, err, "another user's scheduled purchase")
		requireResumeNotTransitioned(t, store)
	})
	t.Run("update-any within its cap resumes", func(t *testing.T) {
		t.Parallel()
		_, _, err := run(revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, capOf(1200)))
		require.NoError(t, err)
	})
	t.Run("update-any above its cap is refused", func(t *testing.T) {
		t.Parallel()
		_, store, err := run(revokePerm(auth.ActionUpdate, nil), revokePerm(auth.ActionUpdateAny, capOf(500)))
		requireRevokeForbidden(t, err, constraintDenied)
		requireResumeNotTransitioned(t, store)
	})
}

// A user API key is capped by its own constraints, not its uncapped owner's.
func TestResumeAmountCap_APIKeyFollowsKeyCap(t *testing.T) {
	t.Parallel()
	rt := keyScopeRoute{name: "resume", method: http.MethodPost, suffix: "/resume", status: "paused"}
	keyPerms := func(cap float64) []auth.Permission {
		return []auth.Permission{revokePerm(auth.ActionUpdate, capOf(cap))}
	}
	t.Run("key cap below the commitment is refused", func(t *testing.T) {
		code, reached := serveKeyScope(t, rt, newKeyScopeAuth(keyPerms(50)), keyScopeOwnerID, keyHeaders())
		assert.Equal(t, http.StatusForbidden, code)
		assert.False(t, reached)
	})
	t.Run("key cap covering the commitment resumes", func(t *testing.T) {
		_, reached := serveKeyScope(t, rt, newKeyScopeAuth(keyPerms(100)), keyScopeOwnerID, keyHeaders())
		assert.True(t, reached)
	})
}

// The admin API key is unrestricted: no amount check and, as on approve, no
// zero-commitment refusal.
func TestResumeAmountCap_AdminAPIKeyUnrestricted(t *testing.T) {
	t.Parallel()
	for name, rec := range map[string]config.RecommendationRecord{
		"above any cap":   resumeCapRec(1e9, 0, "USD"),
		"zero commitment": resumeCapRec(0, 0, "USD"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, 200, resumeAsAdminKey(t, rec))
		})
	}
}

func resumeAsAdminKey(t *testing.T, rec config.RecommendationRecord) int {
	t.Helper()
	ctx := context.Background()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	m := new(MockAuthService)
	t.Cleanup(func() { m.AssertExpectations(t) })
	m.grantAdmin()
	row := cancelRouteExec("paused")
	row.ScheduledDate = time.Now().Add(24 * time.Hour)
	row.Recommendations = []config.RecommendationRecord{rec}
	resumed := *row
	resumed.Status = "pending"
	store.On("GetExecutionByID", mock.Anything, cancelExecID).Return(row, nil)
	store.On("TransitionExecutionStatusPinned", mock.Anything, cancelExecID, []string{"paused"}, "pending", mock.Anything, false).Return(&resumed, nil)
	h := &Handler{config: store, auth: m, corsAllowedOrigin: "*", apiKey: "test-key"}
	resp, err := h.HandleRequest(ctx, &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"X-API-Key": "test-key", "Authorization": "Bearer t", "X-CSRF-Token": "c", "Content-Type": "application/json"},
		RequestContext: events.LambdaFunctionURLRequestContext{HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
			Method: "POST", Path: "/api/purchases/planned/" + cancelExecID + "/resume"}},
	})
	require.NoError(t, err)
	return resp.StatusCode
}

// A nil auth component fails closed with a 500 on the claimed-row path too.
func TestAuthorizeRowManagement_NilAuthIsServerError(t *testing.T) {
	t.Parallel()
	creator := revokeConstraintsUser
	row := &config.PurchaseExecution{CreatedByUserID: &creator}
	verb, err := (&Handler{}).authorizeRowManagement(context.Background(), &Session{UserID: creator}, row)
	ce, ok := IsClientError(err)
	require.True(t, ok, "got %v", err)
	assert.Equal(t, 500, ce.code)
	assert.Empty(t, verb)
}
