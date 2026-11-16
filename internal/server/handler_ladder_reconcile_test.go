package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconcileFakeStore embeds the interface (nil) so only the reconcile methods
// and the global-config read are implemented; any other call panics.
type reconcileFakeStore struct {
	config.StoreInterface
	firedErr error
	// chainStatus is the chain tail's status; empty means "scheduled".
	chainStatus string
	fired       []config.LadderTrancheDB
	listed      int
	global      int
}

func (f *reconcileFakeStore) GetGlobalConfig(context.Context) (*config.GlobalConfig, error) {
	f.global++
	return &config.GlobalConfig{LadderingEnabled: false, LadderExecutionEnabled: false}, nil
}
func (f *reconcileFakeStore) ListFiredLadderTranches(context.Context, *config.LadderFiredCursor, int) ([]config.LadderTrancheDB, error) {
	f.listed++
	return f.fired, f.firedErr
}
func (f *reconcileFakeStore) GetExecutionByID(_ context.Context, id string) (*config.PurchaseExecution, error) {
	acct := "acct-a"
	return &config.PurchaseExecution{
		ExecutionID: id, Status: config.StatusCompleted, CloudAccountID: &acct,
		Recommendations: []config.RecommendationRecord{{Selected: true, PurchaseID: "ri-1", Region: "us-east-1"}},
	}, nil
}
func (f *reconcileFakeStore) WithTx(_ context.Context, fn func(pgx.Tx) error) error { return fn(nil) }
func (f *reconcileFakeStore) LockLadderTrancheChainTx(context.Context, pgx.Tx, string) ([]config.LadderChainNode, error) {
	status := f.chainStatus
	if status == "" {
		status = "scheduled"
	}
	return []config.LadderChainNode{{ExecutionID: "e1", Status: status, CreatedAt: time.Unix(0, 0)}}, nil
}
func (f *reconcileFakeStore) TransitionLadderTrancheTx(context.Context, pgx.Tx, string, ladder.TrancheStatus, ladder.TrancheStatus, string) (bool, error) {
	return true, nil
}

func (f *reconcileFakeStore) GetGlobalConfigTx(context.Context, pgx.Tx) (*config.GlobalConfig, error) {
	f.global++
	return &config.GlobalConfig{LadderingEnabled: false, LadderExecutionEnabled: false}, nil
}
func (f *reconcileFakeStore) DisableLadderConfigTx(context.Context, pgx.Tx, string) (bool, error) {
	return false, nil
}

type reconcileFakePurchase struct {
	PurchaseManagerInterface
}

func (reconcileFakePurchase) LadderSPDescriber(context.Context, string, string) (purchase.SPDescriber, error) {
	return nil, errors.New("no provider in tests")
}

func (reconcileFakePurchase) LadderRIDescriber(context.Context, string, string) (purchase.RIDescriber, error) {
	return nil, errors.New("no provider in tests")
}

func TestParseScheduledEvent_LadderReconcile(t *testing.T) {
	t.Parallel()
	taskType, _, err := ParseScheduledEvent([]byte(`{"action":"ladder_reconcile"}`))
	require.NoError(t, err)
	assert.Equal(t, TaskLadderReconcile, taskType)
}

func TestLadderReconcile_NoFiredTranchesIsNoOpEvenWithSwitchesOff(t *testing.T) {
	t.Parallel()
	store := &reconcileFakeStore{}
	app := &Application{Config: store, Purchase: reconcileFakePurchase{}}

	res, err := app.runScheduledTask(context.Background(), TaskLadderReconcile, ScheduledTaskParams{})

	require.NoError(t, err)
	assert.Equal(t, purchase.LadderReconcileResult{}, res)
	assert.Equal(t, 1, store.listed, "dispatch must reach RunLadderReconcile")
	assert.Zero(t, store.global, "reconcile is not gated by the ladder switches")
}

func TestLadderReconcile_PassesStoreErrorThrough(t *testing.T) {
	t.Parallel()
	boom := errors.New("list failed")
	app := &Application{Config: &reconcileFakeStore{firedErr: boom}, Purchase: reconcileFakePurchase{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.ErrorIs(t, err, boom)
}

func TestLadderReconcile_FailsLoudWhenStoreCannotReconcile(t *testing.T) {
	t.Parallel()
	app := &Application{Config: &ladderTestStore{}, Purchase: reconcileFakePurchase{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.Error(t, err)
}

func TestLadderReconcile_FailsLoudWhenPurchaseCannotDescribeSavingsPlans(t *testing.T) {
	t.Parallel()
	app := &Application{Config: &reconcileFakeStore{}, Purchase: struct{ PurchaseManagerInterface }{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot describe Savings Plans")
}

func TestLadderReconcile_FailsLoudWhenPurchaseCannotDescribeReservedInstances(t *testing.T) {
	t.Parallel()
	app := &Application{Config: &reconcileFakeStore{}, Purchase: spNoRIPurchase{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot describe reserved instances")
}

// spNoRIPurchase has the SP describer but lacks the RI describer method.
type spNoRIPurchase struct{ PurchaseManagerInterface }

func (spNoRIPurchase) LadderSPDescriber(context.Context, string, string) (purchase.SPDescriber, error) {
	return nil, errors.New("no provider in tests")
}

// A describer construction failure leaves the fired RI tranche fired and the
// run reports it unresolved.
func TestLadderReconcile_RIDescriberFailureKeepsRITrancheFired(t *testing.T) {
	t.Parallel()
	exec := "e1"
	store := &reconcileFakeStore{
		chainStatus: config.StatusCompleted, // the state in which visibility is consulted
		fired:       []config.LadderTrancheDB{{ID: "t1", ExecutionID: &exec, LayerType: ladder.LayerConvertibleRI, Status: ladder.TrancheStatusFired}},
	}
	app := &Application{Config: store, Purchase: reconcileFakePurchase{}}

	res, err := app.handleLadderReconcile(context.Background())

	require.NoError(t, err)
	assert.Zero(t, res.Completed)
	assert.Equal(t, 1, res.Unresolved)
}
