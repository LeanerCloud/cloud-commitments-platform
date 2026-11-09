package scheduler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	azureprovider "github.com/LeanerCloud/cloud-commitments-go/providers/azure"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipErr stands in for the GCP provider's unexported partial-result error:
// the scheduler must match the Partial() method, not the type (issue #1472).
type skipErr struct {
	msg     string
	partial bool
	count   int
	reasons []string
}

// kindSkipErr is skipErr that also reports a kind per skip reason, as Azure's
// PartialResultError does (cloud-commitments-go#670).
type kindSkipErr struct {
	skipErr
	kinds []azureprovider.SkipKind
}

func (e *kindSkipErr) SkipKinds() []azureprovider.SkipKind { return e.kinds }

// outageSkip is an Advisor skip whose price lookup did not complete, with a
// reason text that matches no prefix the scheduler ever knew: the hold reads
// the kind, never the text.
func outageSkip(reason string) *kindSkipErr {
	return &kindSkipErr{skipErr: skipErr{partial: true, count: 1, reasons: []string{reason}},
		kinds: []azureprovider.SkipKind{azureprovider.SkipPriceLookupUnavailable}}
}

func (e *skipErr) Error() string         { return e.msg }
func (e *skipErr) Partial() bool         { return e.partial }
func (e *skipErr) SkippedCount() int     { return e.count }
func (e *skipErr) SkipReasons() []string { return e.reasons }

const skipMsg = "gcp recommendations: 3 recommendation(s) skipped: computeengine us-east1: non-USD; cloudsql us-east1: bad plan"

func gcpAmbient(s *Scheduler) ([]config.RecommendationRecord, []string, *sweepGap, error) {
	return s.collectGCPRecommendations(context.Background(), &config.GlobalConfig{})
}

func TestGCPPartialResult_KeepsRecsAndRecordsSkips(t *testing.T) {
	t.Setenv("GCP_PROJECT_ID", "proj-a")
	s, _ := newPartialSweepScheduler(t, "gcp", fmt.Errorf("collect: %w", &skipErr{msg: skipMsg, partial: true, count: 3}))

	recs, roster, gap, err := gcpAmbient(s)

	require.NoError(t, err, "a partial GCP result must not fail the collection run")
	assert.NotEmpty(t, recs, "the recommendations that converted must be kept")
	require.NotNil(t, gap, "the skip must be recorded for the user")
	assert.Equal(t, 3, gap.skippedRecommendations)
	assert.Equal(t, 3, gap.toProviderSweep("gcp").SkippedRecommendations)
	assert.NotEmpty(t, roster,
		"skipped recommendations are invalid, so the account stays eviction-eligible and their stale rows go")
	assert.Zero(t, gap.forAccount().incompleteAccounts, "a skip is not an incomplete account")
}

func TestGCPPartialResult_NonPartialErrorsStayHard(t *testing.T) {
	for name, err := range map[string]error{
		"plain error":        errors.New("boom"),
		"Partial() == false": &skipErr{msg: skipMsg, partial: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("GCP_PROJECT_ID", "proj-a")
			s, _ := newPartialSweepScheduler(t, "gcp", err)

			recs, roster, gap, gotErr := gcpAmbient(s)

			require.Error(t, gotErr, "a non-partial error must fail the run")
			assert.Empty(t, recs)
			assert.Empty(t, roster)
			assert.Nil(t, gap)
		})
	}
}

func TestGCPPartialResult_FanOutKeepsAccountEvictable(t *testing.T) {
	accounts := []config.CloudAccount{{ID: "a1", Name: "a1", ExternalID: "p1"}, {ID: "a2", Name: "a2", ExternalID: "p2"}}
	fn := func(_ context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error) {
		gap, err := tolerateIncompleteSweep("gcp", &skipErr{msg: skipMsg, partial: true, count: 3})
		if acct.ID == "a2" {
			// A lost project in the same account hold back eviction as before.
			gap = mergeSweepGaps(gap, gapFromPartialSubscriptions(partialSweepErr("p-x")))
		}
		return []config.RecommendationRecord{{ID: acct.ID}}, gap, err
	}

	recs, outcome := fanOutPerAccount(context.Background(), "GCP", accounts, fn)

	assert.Len(t, recs, 2)
	assert.Equal(t, []string{"a1"}, outcome.SucceededAccountIDs)
	assert.Equal(t, []string{"a2"}, outcome.IncompleteAccountIDs)
	require.NotNil(t, outcome.Gap)
	assert.Equal(t, 6, outcome.Gap.skippedRecommendations)
	assert.Equal(t, 1, outcome.Gap.incompleteAccounts)
}

// The count comes from SkippedCount(), never from the message text.
func TestGCPPartialResult_CountComesFromTypedMethod(t *testing.T) {
	gap, err := tolerateIncompleteSweep("gcp", &skipErr{msg: "something skipped", partial: true, count: 5})
	require.NoError(t, err)
	require.NotNil(t, gap)
	assert.Equal(t, 5, gap.skippedRecommendations)
}

// A mixed Azure sweep (lost subscriptions plus recommendations dropped on the
// reachable ones) reports the skipped count for the user warning, but the lost
// subscriptions still hold stale-row eviction back.
func TestAzureMixedSweep_RecordsSkipsAndHoldsEviction(t *testing.T) {
	failure := azureprovider.NewPartialSubscriptionFailure(2,
		[]azureprovider.SubscriptionFailure{{SubscriptionID: "sub-b", Err: errors.New("throttled")}},
		[]string{"sub-a: non-USD", "sub-a: bad term"})

	gap, err := tolerateIncompleteSweep("azure", failure)

	require.NoError(t, err)
	require.NotNil(t, gap)
	assert.Equal(t, 2, gap.skippedRecommendations)
	assert.Equal(t, 2, gap.toProviderSweep("azure").SkippedRecommendations)
	assert.True(t, gap.holdsEviction(), "lost subscriptions still hold stale-row eviction")
	assert.Equal(t, 1, gap.forAccount().incompleteAccounts)
}

func TestAzureFailureWithoutSkips_RecordsNoSkippedCount(t *testing.T) {
	failure := azureprovider.NewPartialSubscriptionFailure(2,
		[]azureprovider.SubscriptionFailure{{SubscriptionID: "sub-b", Err: errors.New("throttled")}}, nil)

	gap, err := tolerateIncompleteSweep("azure", failure)

	require.NoError(t, err)
	require.NotNil(t, gap)
	assert.Zero(t, gap.skippedRecommendations)
	assert.True(t, gap.holdsEviction())
}

func TestCountPricingOutageSkips(t *testing.T) {
	const (
		outage     = azureprovider.SkipPriceLookupUnavailable
		unpriced   = azureprovider.SkipUnpriceable
		incomplete = azureprovider.SkipSourceIncomplete
	)
	for name, tc := range map[string]struct {
		kinds []azureprovider.SkipKind
		want  int
	}{
		"a failed price lookup":                 {[]azureprovider.SkipKind{outage}, 1},
		"later rows of the same call":           {[]azureprovider.SkipKind{unpriced, outage, outage}, 2},
		"invalid recommendations only":          {[]azureprovider.SkipKind{unpriced, unpriced}, 0},
		"a truncated walk is not an outage":     {[]azureprovider.SkipKind{incomplete}, 0},
		"a truncated walk plus an outage":       {[]azureprovider.SkipKind{incomplete, outage}, 1},
		"an unrecognized kind is not an outage": {[]azureprovider.SkipKind{azureprovider.SkipKind("a_kind_added_later")}, 0},
		"none":                                  {nil, 0},
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, tc.want, countPricingOutageSkips(tc.kinds)) })
	}
}

// A pricing outage holds eviction; ordinary invalid-row skips do not.
func TestTolerateSkipped_PricingOutageHoldsEviction(t *testing.T) {
	gap, err := tolerateIncompleteSweep("azure", outageSkip("advisor: price lookup unavailable: x"))
	require.NoError(t, err)
	assert.True(t, gap.holdsEviction())
	assert.Equal(t, 1, gap.skippedRecommendations)

	gap, err = tolerateIncompleteSweep("azure", &skipErr{partial: true, count: 1, reasons: []string{"advisor: non-USD"}})
	require.NoError(t, err)
	assert.False(t, gap.holdsEviction())
}

// The held account's platform ID travels with the gap through the fan-out;
// accounts whose skips are ordinary invalid rows are not held.
func TestPricingOutageGap_FanOutRecordsHeldAccounts(t *testing.T) {
	accounts := []config.CloudAccount{{ID: "held", Name: "held"}, {ID: "plain", Name: "plain"}}
	fn := func(_ context.Context, acct config.CloudAccount) ([]config.RecommendationRecord, *sweepGap, error) {
		var skip error = &skipErr{partial: true, count: 1, reasons: []string{"advisor: non-USD"}}
		if acct.ID == "held" {
			skip = outageSkip("advisor: price lookup unavailable: x")
		}
		gap, err := tolerateIncompleteSweep("azure", skip)
		return []config.RecommendationRecord{{ID: acct.ID}}, gap, err
	}

	_, outcome := fanOutPerAccount(context.Background(), "Azure", accounts, fn)

	require.NotNil(t, outcome.Gap)
	sweep := outcome.Gap.toProviderSweep("azure")
	assert.Equal(t, []string{"held"}, sweep.HeldAccountIDs)
	assert.Equal(t, 1, sweep.IncompleteAccounts)
	assert.Equal(t, 1, sweep.Reasons[config.PartialReasonPricingUnavailable])
	assert.Equal(t, []string{"held"}, outcome.IncompleteAccountIDs)
	assert.Equal(t, []string{"plain"}, outcome.SucceededAccountIDs)
}

// The hold decision reads only the typed kind. An outage skip whose reason text
// matches no known prefix still holds, and a skip whose text looks like an
// outage but is not of that kind does not: the library can reword its reasons
// without silently stopping or starting the hold.
func TestTolerateSkipped_HoldReadsTheKindNotTheText(t *testing.T) {
	gap, err := tolerateIncompleteSweep("azure", outageSkip("the retail catalog said no"))
	require.NoError(t, err)
	assert.True(t, gap.holdsEviction(), "an outage with unfamiliar text still holds")
	assert.True(t, gap.pricingUnavailable)

	lookalike := &kindSkipErr{skipErr: skipErr{partial: true, count: 1, reasons: []string{"advisor: price lookup unavailable: x"}},
		kinds: []azureprovider.SkipKind{azureprovider.SkipUnpriceable}}
	gap, err = tolerateIncompleteSweep("azure", lookalike)
	require.NoError(t, err)
	assert.False(t, gap.holdsEviction(), "outage-looking text of another kind does not hold")

	// Kinds beyond the skips name no skip: the library ignores them, so they
	// must not count as an outage.
	extra := &kindSkipErr{skipErr: skipErr{partial: true, count: 1, reasons: []string{"a"}},
		kinds: []azureprovider.SkipKind{azureprovider.SkipUnpriceable, azureprovider.SkipPriceLookupUnavailable}}
	gap, err = tolerateIncompleteSweep("azure", extra)
	require.NoError(t, err)
	assert.False(t, gap.holdsEviction(), "a kind with no skip behind it never holds")

	short := &kindSkipErr{skipErr: skipErr{partial: true, count: 2, reasons: []string{"a", "b"}},
		kinds: []azureprovider.SkipKind{azureprovider.SkipUnpriceable}}
	gap, err = tolerateIncompleteSweep("azure", short)
	require.NoError(t, err)
	assert.False(t, gap.holdsEviction(), "a skip with no kind reads as unpriceable, never as an outage")
}

// The same through the multi-subscription failure that also lost a subscription.
func TestGapFromPartialSubscriptions_HoldReadsTheKindNotTheText(t *testing.T) {
	failed := []azureprovider.SubscriptionFailure{{SubscriptionID: "sub-b", Err: errors.New("403 authorization failed")}}
	gap, err := tolerateIncompleteSweep("azure", azureprovider.NewPartialSubscriptionFailureWithKinds(2, failed,
		[]string{"the retail catalog said no"}, []azureprovider.SkipKind{azureprovider.SkipPriceLookupUnavailable}))
	require.NoError(t, err)
	assert.True(t, gap.forAccount().withHeld("acct-1").pricingUnavailable)

	gap, err = tolerateIncompleteSweep("azure", azureprovider.NewPartialSubscriptionFailure(2, failed,
		[]string{"advisor: price lookup unavailable: x"}))
	require.NoError(t, err)
	assert.False(t, gap.forAccount().withHeld("acct-1").pricingUnavailable, "no kinds recorded: the text alone never holds")
}
