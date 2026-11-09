package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dupTupleExec(recs ...config.RecommendationRecord) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID: "rs-dup", IdempotencyKey: "rs-dup-lineage", Status: "running", Recommendations: recs,
	}
}

func TestRedshiftDuplicateTuple_RefusedBeforeSubmission(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	exec := dupTupleExec(redshiftScopedRec("acct-A"), redshiftScopedRec("acct-A"))
	run := runRedshiftFinalize(t, exec, nil, common.ServiceRedshift, stampAt(at))

	require.Error(t, run.err)
	assert.Equal(t, redshiftDuplicateTupleRefusal, run.err.Error())
	assert.True(t, isBeforeSubmission(run.err))
	assert.True(t, exec.NoSubmission, "nothing was submitted, so the row is retryable from scratch")
	assert.Equal(t, "failed", exec.Status)
	assert.Empty(t, run.opts, "no PurchaseCommitment call")
	assert.Empty(t, run.stampedIDs, "refused before the dispatch gate stamps")
}

func TestRedshiftDuplicateTuple_Detection(t *testing.T) {
	a := redshiftScopedRec("acct-A")
	diff := func(f func(*config.RecommendationRecord)) config.RecommendationRecord {
		r := redshiftScopedRec("acct-A")
		f(&r)
		return r
	}
	unselected := redshiftScopedRec("acct-A")
	unselected.Selected = false
	ec2 := scopedTestRec("acct-A")
	noAcct := redshiftScopedRec("acct-A")
	noAcct.CloudAccountID = nil

	cases := map[string]struct {
		recs    []config.RecommendationRecord
		refused bool
	}{
		"identical tuple and account":   {[]config.RecommendationRecord{a, a}, true},
		"identical, third one differs":  {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.Region = "eu-west-1" }), a}, true},
		"different node type":           {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.ResourceType = "ra3.4xlarge" })}, false},
		"different region":              {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.Region = "eu-west-1" })}, false},
		"different term":                {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.Term = 3 })}, false},
		"different payment":             {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.Payment = "no_upfront" })}, false},
		"different count":               {[]config.RecommendationRecord{a, diff(func(r *config.RecommendationRecord) { r.Count = 2 })}, false},
		"different account":             {[]config.RecommendationRecord{a, redshiftScopedRec("acct-B")}, false},
		"duplicate is not selected":     {[]config.RecommendationRecord{a, unselected}, false},
		"non-redshift twins":            {[]config.RecommendationRecord{ec2, ec2}, false},
		"single rec":                    {[]config.RecommendationRecord{a}, false},
		"both without an account":       {[]config.RecommendationRecord{noAcct, noAcct}, true},
		"one scoped, one without scope": {[]config.RecommendationRecord{a, noAcct}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := &Manager{config: new(MockConfigStore), attemptJournal: &fakeAttemptJournal{}}
			err := m.preExecutionRefusal(context.Background(), dupTupleExec(tc.recs...))
			if tc.refused {
				require.Error(t, err)
				assert.Equal(t, redshiftDuplicateTupleRefusal, err.Error())
				assert.True(t, isBeforeSubmission(err))
				return
			}
			assert.NoError(t, err)
		})
	}
}

// A rec with no account of its own buys in the execution's account, so it duplicates a scoped twin there.
func TestRedshiftDuplicateTuple_UnscopedRecUsesTheExecutionAccount(t *testing.T) {
	acct := "acct-A"
	noAcct := redshiftScopedRec("acct-A")
	noAcct.CloudAccountID = nil
	exec := dupTupleExec(redshiftScopedRec("acct-A"), noAcct)
	exec.CloudAccountID = &acct

	err := (&Manager{config: new(MockConfigStore)}).preExecutionRefusal(context.Background(), exec)
	require.Error(t, err)
	assert.Equal(t, redshiftDuplicateTupleRefusal, err.Error())

	other := "acct-B"
	exec.CloudAccountID = &other
	assert.NoError(t, (&Manager{config: new(MockConfigStore)}).preExecutionRefusal(context.Background(), exec))
}
