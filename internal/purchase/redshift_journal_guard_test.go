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

func journalGuardExec(attempt int, rec config.RecommendationRecord) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID: "rs-retry", IdempotencyKey: "rs-lineage", Status: "running", RetryAttemptN: attempt,
		Recommendations: []config.RecommendationRecord{rec},
	}
}

func TestRedshiftRetryDispatch_NilJournalRefused(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	exec := journalGuardExec(1, redshiftScopedRec("acct-A"))
	run := runRedshiftFinalize(t, exec, nil, common.ServiceRedshift, stampAt(at))

	require.Error(t, run.err)
	assert.Equal(t, redshiftNoJournalRefusal, run.err.Error())
	assert.False(t, isBeforeSubmission(run.err), "an earlier attempt may have sent, so no_submission must stay false")
	assert.False(t, exec.NoSubmission)
	assert.Equal(t, "failed", exec.Status)
	assert.Empty(t, run.opts, "no PurchaseCommitment call")
	assert.Empty(t, run.stampedIDs, "no StampFirstDispatchTx call")
}

func TestRedshiftRetryDispatch_WithJournalDispatches(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	journal := &fakeAttemptJournal{}
	run := runRedshiftFinalizeWithJournal(t, journalGuardExec(1, redshiftScopedRec("acct-A")), nil, common.ServiceRedshift, stampAt(at), journal)

	require.NoError(t, run.err)
	require.Len(t, run.opts, 1)
	assert.Same(t, journal, run.opts[0].AttemptJournal)
	assert.True(t, run.opts[0].RequestedAt.Equal(at))
}

func TestRedshiftAttempt0Dispatch_NilJournalStillDispatches(t *testing.T) {
	at := time.Now().UTC().Add(-time.Minute)
	run := runRedshiftFinalize(t, journalGuardExec(0, redshiftScopedRec("acct-A")), nil, common.ServiceRedshift, stampAt(at))

	require.NoError(t, run.err)
	require.Len(t, run.opts, 1)
	assert.Nil(t, run.opts[0].AttemptJournal)
}

func TestNonRedshiftRetryDispatch_NilJournalUnaffected(t *testing.T) {
	rds := scopedTestRec("acct-A")
	rds.Service = "rds"
	m := &Manager{config: new(MockConfigStore)}

	assert.NoError(t, m.preExecutionRefusal(context.Background(), journalGuardExec(1, rds)))
}
