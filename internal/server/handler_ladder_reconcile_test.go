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
	fired    []config.LadderTrancheDB
	firedErr error
	listed   int
	global   int
}

func (f *reconcileFakeStore) GetGlobalConfig(context.Context) (*config.GlobalConfig, error) {
	f.global++
	return &config.GlobalConfig{LadderingEnabled: false, LadderExecutionEnabled: false}, nil
}
func (f *reconcileFakeStore) ListFiredLadderTranches(context.Context, *config.LadderFiredCursor, int) ([]config.LadderTrancheDB, error) {
	f.listed++
	return f.fired, f.firedErr
}
func (f *reconcileFakeStore) WithTx(_ context.Context, fn func(pgx.Tx) error) error { return fn(nil) }
func (f *reconcileFakeStore) LockLadderTrancheChainTx(context.Context, pgx.Tx, string) ([]config.LadderChainNode, error) {
	return []config.LadderChainNode{{ExecutionID: "e1", Status: "scheduled", CreatedAt: time.Unix(0, 0)}}, nil
}
func (f *reconcileFakeStore) TransitionLadderTrancheTx(context.Context, pgx.Tx, string, ladder.TrancheStatus, ladder.TrancheStatus, string) (bool, error) {
	return true, nil
}

type reconcileFakePurchase struct {
	PurchaseManagerInterface
}

func (reconcileFakePurchase) LadderSPDescriber(context.Context, string, string) (purchase.SPDescriber, error) {
	return nil, errors.New("no provider in tests")
}

func TestParseScheduledEvent_LadderReconcile(t *testing.T) {
	taskType, _, err := ParseScheduledEvent([]byte(`{"action":"ladder_reconcile"}`))
	require.NoError(t, err)
	assert.Equal(t, TaskLadderReconcile, taskType)
}

func TestLadderReconcile_NoFiredTranchesIsNoOpEvenWithSwitchesOff(t *testing.T) {
	store := &reconcileFakeStore{}
	app := &Application{Config: store, Purchase: reconcileFakePurchase{}}

	res, err := app.runScheduledTask(context.Background(), TaskLadderReconcile, ScheduledTaskParams{})

	require.NoError(t, err)
	assert.Equal(t, purchase.LadderReconcileResult{}, res)
	assert.Equal(t, 1, store.listed, "dispatch must reach RunLadderReconcile")
	assert.Zero(t, store.global, "reconcile is not gated by the ladder switches")
}

func TestLadderReconcile_PassesStoreErrorThrough(t *testing.T) {
	boom := errors.New("list failed")
	app := &Application{Config: &reconcileFakeStore{firedErr: boom}, Purchase: reconcileFakePurchase{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.ErrorIs(t, err, boom)
}

func TestLadderReconcile_FailsLoudWhenStoreCannotReconcile(t *testing.T) {
	app := &Application{Config: &ladderTestStore{}, Purchase: reconcileFakePurchase{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.Error(t, err)
}

func TestLadderReconcile_FailsLoudWhenPurchaseCannotDescribeSavingsPlans(t *testing.T) {
	app := &Application{Config: &reconcileFakeStore{}, Purchase: struct{ PurchaseManagerInterface }{}}

	_, err := app.handleLadderReconcile(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot describe Savings Plans")
}
