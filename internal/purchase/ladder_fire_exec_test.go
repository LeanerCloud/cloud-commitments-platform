package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeExecSaver struct {
	saved []*config.PurchaseExecution
	err   error
}

func (f *fakeExecSaver) SavePurchaseExecutionTx(_ context.Context, _ pgx.Tx, e *config.PurchaseExecution) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, e)
	return nil
}

func hourlyOf(t *testing.T, r config.RecommendationRecord) float64 {
	t.Helper()
	decoded, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	require.NoError(t, err)
	d, ok := decoded.(*common.SavingsPlanDetails)
	require.True(t, ok)
	return d.HourlyCommitment
}

var fireNow = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func TestLadderClaimCreate_ExecutionFields(t *testing.T) {
	saver := &fakeExecSaver{}
	execs := map[string]*config.PurchaseExecution{}
	cfg := resolveCfg(10)
	create := ladderClaimCreate(saver, NewLadderInstrumentRegistry(), cfg, "eu-west-1", 48*time.Hour, fireNow, execs)

	tr := spTranche(2.5, ladder.Term1Year)
	id, err := create(context.Background(), nil, tr)
	require.NoError(t, err)
	require.Len(t, saver.saved, 1)
	e := saver.saved[0]

	assert.Equal(t, e.ExecutionID, id)
	assert.NotEmpty(t, id)
	assert.Same(t, e, execs[tr.ID])
	assert.Empty(t, e.PlanID)
	assert.Equal(t, "scheduled", e.Status)
	assert.Equal(t, 0, e.StepNumber)
	assert.Equal(t, fireNow, e.ScheduledDate)
	require.NotNil(t, e.ScheduledExecutionAt)
	assert.Equal(t, fireNow.Add(48*time.Hour), *e.ScheduledExecutionAt)
	assert.Nil(t, e.NotificationSent)
	assert.Empty(t, e.ApprovalToken)
	assert.Nil(t, e.ApprovedBy)
	assert.Nil(t, e.CreatedByUserID)
	assert.True(t, e.CreatedBySystem)
	assert.Equal(t, tr.ID, e.IdempotencyKey)
	assert.Zero(t, e.TotalUpfrontCost)
	assert.Zero(t, e.EstimatedSavings)
	assert.Empty(t, e.Source)
	assert.False(t, e.NoSubmission)
	require.NotNil(t, e.CloudAccountID)
	assert.Equal(t, "acct-a", *e.CloudAccountID)

	require.Len(t, e.Recommendations, 1)
	r := e.Recommendations[0]
	assert.Equal(t, "eu-west-1", r.Region)
	assert.Equal(t, 1, r.Term)
	assert.Equal(t, "no-upfront", r.Payment)
	require.NotNil(t, r.CloudAccountID)
	assert.Equal(t, "acct-a", *r.CloudAccountID)
	require.NotNil(t, r.MonthlyCost)
	assert.InDelta(t, spMonthlyCost(2.5, 1), *r.MonthlyCost, 1e-9)
	assert.InDelta(t, 2.5, hourlyOf(t, r), 1e-9)
}

func TestLadderClaimCreate_BuildsFromTheLockedRow(t *testing.T) {
	saver := &fakeExecSaver{}
	create := ladderClaimCreate(saver, NewLadderInstrumentRegistry(), resolveCfg(10), "us-east-1", 24*time.Hour, fireNow, map[string]*config.PurchaseExecution{})
	// The resolve step saw 2.5; the locked row now says 3.1.
	_, err := create(context.Background(), nil, spTranche(3.1, ladder.Term1Year))
	require.NoError(t, err)
	assert.InDelta(t, 3.1, hourlyOf(t, saver.saved[0].Recommendations[0]), 1e-9)
}

func TestLadderClaimCreate_BuildFailureSavesNothing(t *testing.T) {
	saver := &fakeExecSaver{}
	execs := map[string]*config.PurchaseExecution{}
	create := ladderClaimCreate(saver, NewLadderInstrumentRegistry(), resolveCfg(10), "", 24*time.Hour, fireNow, execs)
	id, err := create(context.Background(), nil, spTranche(2.5, ladder.Term1Year))
	require.Error(t, err)
	assert.Empty(t, id)
	assert.Empty(t, saver.saved)
	assert.Empty(t, execs)
}

func TestLadderClaimCreate_SaveErrorIsReturned(t *testing.T) {
	boom := errors.New("boom")
	execs := map[string]*config.PurchaseExecution{}
	create := ladderClaimCreate(&fakeExecSaver{err: boom}, NewLadderInstrumentRegistry(), resolveCfg(10), "us-east-1", 24*time.Hour, fireNow, execs)
	id, err := create(context.Background(), nil, spTranche(2.5, ladder.Term1Year))
	require.ErrorIs(t, err, boom)
	assert.Empty(t, id)
	assert.Empty(t, execs)
}
