package purchase

// Redshift re-drive end to end: the fake client models go's no-journal window dedupe (the
// journal is present only for the C10b check) and the store is the mock; no cloud API is reached.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// dedupingRedshiftClient mirrors go's adoption rule: a node started at or after
// RequestedAt minus the 5m skew is adopted instead of bought again.
type dedupingRedshiftClient struct {
	*MockServiceClient
	mu           sync.Mutex
	opts         []common.PurchaseOptions
	nodes        []time.Time
	nodeStart    time.Time
	loseResponse bool
}

func (c *dedupingRedshiftClient) PurchaseCommitment(_ context.Context, _ common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opts = append(c.opts, opts)
	for _, start := range c.nodes {
		if !start.Before(opts.RequestedAt.Add(-5 * time.Minute)) {
			return common.PurchaseResult{Success: true, CommitmentID: "adopted-node"}, nil
		}
	}
	c.nodes = append(c.nodes, c.nodeStart)
	if c.loseResponse {
		c.loseResponse = false
		return common.PurchaseResult{}, errors.New("purchase outcome unknown: the request may have been sent")
	}
	return common.PurchaseResult{Success: true, CommitmentID: "new-node"}, nil
}

type redriveRig struct {
	mgr     *Manager
	store   *MockConfigStore
	client  *dedupingRedshiftClient
	journal *fakeAttemptJournal
	saves   []config.PurchaseExecution
	minted  map[string]time.Time
	rows    map[string]*config.PurchaseExecution
}

// coalescingStore mirrors StampFirstDispatchTx's COALESCE: a row's own stamp wins over a minted one.
type coalescingStore struct {
	*MockConfigStore
	rig *redriveRig
}

func (s coalescingStore) StampFirstDispatchTx(_ context.Context, _ pgx.Tx, id string, _ time.Duration) (config.StampResult, error) {
	if row := s.rig.rows[id]; row != nil && row.FirstDispatchAt != nil {
		at := *row.FirstDispatchAt
		return config.StampResult{At: &at, WithinHorizon: true}, nil
	}
	minted, ok := s.rig.minted[id]
	if !ok {
		return config.StampResult{}, nil
	}
	return config.StampResult{At: &minted, WithinHorizon: true}, nil
}

// track registers the row the store sees under its ID, by pointer so a gate-set stamp is visible.
func (r *redriveRig) track(row *config.PurchaseExecution) *config.PurchaseExecution {
	r.rows[row.ExecutionID] = row
	return row
}

// newRedriveRig wires a manager through ManagerConfig. stamps maps an execution ID to the first
// dispatch time the store mints for it when the row has none; conflicts is what the gate query finds.
func newRedriveRig(t *testing.T, stamps map[string]time.Time, conflicts []config.RedshiftLineageConflict) *redriveRig {
	t.Helper()
	rig := &redriveRig{
		store:   new(MockConfigStore),
		client:  &dedupingRedshiftClient{MockServiceClient: new(MockServiceClient)},
		journal: &fakeAttemptJournal{},
	}
	email := new(MockEmailSender)
	factory := new(MockProviderFactory)
	prov := new(MockProvider)
	var mu sync.Mutex
	rig.store.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		mu.Lock()
		rig.saves = append(rig.saves, *e)
		mu.Unlock()
		return nil
	}
	rig.store.On("LockRedshiftTuplesTx", mock.Anything, mock.Anything, mock.Anything).Return(nil)
	rig.store.On("FindOpenRedshiftLineageConflictsTx", mock.Anything, mock.Anything, mock.Anything, mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(conflicts, nil)
	rig.minted, rig.rows = stamps, map[string]*config.PurchaseExecution{}
	rig.store.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Maybe()
	email.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	email.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
	prov.On("GetServiceClient", mock.Anything, common.ServiceRedshift, mock.Anything).Return(rig.client, nil).Maybe()
	rig.mgr = NewManager(ManagerConfig{
		ConfigStore: coalescingStore{rig.store, rig}, EmailSender: email, ProviderFactory: factory, CredentialStore: awsAccessKeyCredStore(),
		DashboardURL: "https://dashboard.example.com", AttemptJournal: rig.journal,
	})
	return rig
}

func redriveExec(id string, attempt int, stamp *time.Time) *config.PurchaseExecution {
	return &config.PurchaseExecution{
		ExecutionID: id, IdempotencyKey: "rs-lineage", Status: "running", RetryAttemptN: attempt,
		FirstDispatchAt: stamp, Recommendations: []config.RecommendationRecord{redshiftScopedRec("acct-A")},
	}
}

// The first attempt loses its response after AWS accepted the order. The retry successor copies the
// lineage stamp, so go sees the original RequestedAt and adopts the node instead of buying a second one.
func TestRedshiftRedrive_AdoptsOriginalNodeWithOriginalRequestedAt(t *testing.T) {
	stamp := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Microsecond)
	rig := newRedriveRig(t, map[string]time.Time{"attempt-0": stamp, "attempt-1": time.Now().UTC()}, nil)
	rig.client.nodeStart, rig.client.loseResponse = stamp.Add(time.Minute), true

	first := rig.track(redriveExec("attempt-0", 0, nil))
	require.Error(t, rig.mgr.executeAndFinalize(context.Background(), first))
	require.NotNil(t, first.FirstDispatchAt, "the gate minted the lineage stamp")

	successor := rig.track(redriveExec("attempt-1", 1, first.FirstDispatchAt))
	require.NoError(t, rig.mgr.executeAndFinalize(context.Background(), successor))

	require.Len(t, rig.client.opts, 2)
	assert.Len(t, rig.client.nodes, 1, "the retry adopted the first node instead of buying another")
	assert.True(t, rig.client.opts[0].RequestedAt.Equal(stamp))
	assert.True(t, rig.client.opts[1].RequestedAt.Equal(rig.client.opts[0].RequestedAt), "the retry keeps the original RequestedAt")
	for _, o := range rig.client.opts {
		assert.Same(t, rig.journal, o.AttemptJournal, "the manager's journal reaches every purchase call")
	}
	assert.Equal(t, "completed", successor.Status)
}

// While the first lineage's failed row can still be retried, a second lineage for the same
// tuple is refused before any provider call and proven unsent.
func TestRedshiftRedrive_SecondLineageRefusedWhileFirstRetryable(t *testing.T) {
	stamp := time.Now().UTC().Add(-time.Hour)
	rig := newRedriveRig(t, map[string]time.Time{"lineage-2": stamp},
		[]config.RedshiftLineageConflict{{ExecutionID: "lineage-1-failed", Status: config.StatusFailed, Error: "context deadline exceeded"}})

	second := redriveExec("lineage-2", 0, nil)
	err := rig.mgr.executeAndFinalize(context.Background(), second)

	require.Error(t, err)
	assert.Equal(t, redshiftFailedConflictRefusal, err.Error())
	assert.True(t, isBeforeSubmission(err))
	assert.Empty(t, rig.client.opts)
	assert.True(t, second.NoSubmission)
}

func strandedRedshiftRow(id string, attempt int, stamp *time.Time) config.PurchaseExecution {
	row := redriveExec(id, attempt, stamp)
	row.Status = "approved"
	return *row
}

func recoverStranded(t *testing.T, rig *redriveRig, row config.PurchaseExecution) {
	t.Helper()
	ctx := context.Background()
	claimed := row
	claimed.Status = "running"
	rig.track(&claimed)
	rig.store.On("GetStaleApprovedExecutions", ctx, staleApprovedThreshold).Return([]config.PurchaseExecution{row}, nil)
	rig.store.On("TransitionExecutionStatus", ctx, row.ExecutionID, []string{"approved"}, "running", (*string)(nil)).Return(&claimed, nil)

	recovered, err := rig.mgr.RecoverStrandedApprovals(ctx)

	require.NoError(t, err)
	require.Equal(t, 1, recovered)
}

func TestRedshiftRedrive_RecoverStrandedApprovals_Attempt0MintsStamp(t *testing.T) {
	minted := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	rig := newRedriveRig(t, map[string]time.Time{"stranded-0": minted}, nil)
	rig.client.nodeStart = minted

	recoverStranded(t, rig, strandedRedshiftRow("stranded-0", 0, nil))

	require.Len(t, rig.client.opts, 1)
	assert.True(t, rig.client.opts[0].RequestedAt.Equal(minted), "an attempt 0 row never sent, so it mints the lineage stamp")
	assert.Same(t, rig.journal, rig.client.opts[0].AttemptJournal)
}

func TestRedshiftRedrive_RecoverStrandedApprovals_SuccessorKeepsCopiedStamp(t *testing.T) {
	copied := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Microsecond)
	rig := newRedriveRig(t, map[string]time.Time{"stranded-1": time.Now().UTC()}, nil)
	rig.client.nodeStart = copied.Add(time.Minute)

	recoverStranded(t, rig, strandedRedshiftRow("stranded-1", 1, &copied))

	require.Len(t, rig.client.opts, 1)
	assert.True(t, rig.client.opts[0].RequestedAt.Equal(copied), "a successor dispatches under the copied stamp, not a new one")
	assert.Same(t, rig.journal, rig.client.opts[0].AttemptJournal)
}

// A successor created before the stamp was copied has none; the lift must not let it re-dispatch.
func TestRedshiftRedrive_PreChangeSuccessorWithoutStampIsDefusedAfterLift(t *testing.T) {
	rig := newRedriveRig(t, map[string]time.Time{}, nil)
	legacy := rig.track(redriveExec("legacy-successor", 1, nil))
	err := rig.mgr.executeAndFinalize(context.Background(), legacy)

	require.Error(t, err)
	assert.Equal(t, redshiftNoStampRefusal, err.Error())
	assert.False(t, isBeforeSubmission(err), "an earlier attempt may have sent")
	assert.Empty(t, rig.client.opts)
	assert.Equal(t, "failed", legacy.Status)
}
