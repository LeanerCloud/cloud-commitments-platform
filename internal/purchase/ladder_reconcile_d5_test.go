package purchase

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func switchesOn() *config.GlobalConfig {
	return &config.GlobalConfig{LadderingEnabled: true, LadderExecutionEnabled: true}
}

func TestLadderD5Exempt(t *testing.T) {
	on := switchesOn
	cases := []struct {
		name       string
		global     func() *config.GlobalConfig
		layer      ladder.LayerType
		reason     string
		wantSwitch string
		wantExempt bool
	}{
		{"laddering off", func() *config.GlobalConfig { g := on(); g.LadderingEnabled = false; return g }, ladder.LayerComputeSP, ladderReasonNoSubmission, "laddering_enabled", true},
		{"execution off", func() *config.GlobalConfig { g := on(); g.LadderExecutionEnabled = false; return g }, ladder.LayerComputeSP, ladderReasonNoSubmission, "ladder_execution_enabled", true},
		{"4-eyes on", func() *config.GlobalConfig { g := on(); g.RequireDifferentApprover = true; return g }, ladder.LayerComputeSP, ladderReasonNoSubmission, "require_different_approver", true},
		{"RI layer with standard class", func() *config.GlobalConfig { g := on(); g.OfferingClass = "standard"; return g }, ladder.LayerConvertibleRI, ladderReasonNoSubmission, "offering_class", true},
		{"RI layer with empty class", on, ladder.LayerConvertibleRI, ladderReasonNoSubmission, "", false},
		{"RI layer with convertible class", func() *config.GlobalConfig { g := on(); g.OfferingClass = "convertible"; return g }, ladder.LayerConvertibleRI, ladderReasonNoSubmission, "", false},
		{"class only matters for RI layers", func() *config.GlobalConfig { g := on(); g.OfferingClass = "standard"; return g }, ladder.LayerComputeSP, ladderReasonNoSubmission, "", false},
		{"all switches fine", on, ladder.LayerComputeSP, ladderReasonNoSubmission, "", false},
		{"canceled is never exempt", func() *config.GlobalConfig { return &config.GlobalConfig{RequireDifferentApprover: true} }, ladder.LayerComputeSP, ladderReasonApprovalCanceled, "", false},
		{"expired is never exempt", func() *config.GlobalConfig { return &config.GlobalConfig{RequireDifferentApprover: true} }, ladder.LayerComputeSP, ladderReasonApprovalExpired, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sw, exempt := ladderD5Exempt(c.global(), c.layer, c.reason)
			assert.Equal(t, c.wantExempt, exempt)
			assert.Equal(t, c.wantSwitch, sw)
		})
	}
}

// d5Fake serves tranches t1..tN, each with its own config c1..cN unless a test
// overrides ConfigID, and a global config with every switch on.
func d5Fake(status map[string]string, ids ...string) *pagingFake {
	tranches := pagingTranches(ids...)
	for i := range tranches {
		cfg := fmt.Sprintf("c%d", i+1)
		tranches[i].ConfigID = &cfg
		tranches[i].LayerType = ladder.LayerComputeSP
	}
	return &pagingFake{tranches: tranches, status: status, global: switchesOn()}
}

type d5Vis struct{ visible bool }

func (v d5Vis) CommitmentVisible(context.Context, config.LadderTrancheDB, string) (bool, error) {
	return v.visible, nil
}

func sweep(t *testing.T, f *pagingFake) (LadderReconcileResult, error) {
	t.Helper()
	return ReconcileLadderTranches(context.Background(), f, d5Vis{}, testNow)
}

func TestReconcileD5_CanceledDisablesWithoutReadingGlobal(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "canceled"}, "t1")
	f.global = &config.GlobalConfig{} // every switch off: a revoke still disables.

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, []string{"c1"}, f.disabled)
	assert.Zero(t, f.globalReads)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1, ConfigsDisabled: 1}, res)
}

func TestReconcileD5_NoSubmissionWithSwitchesOnDisables(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "failed"}, "t1")

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, 1, f.globalReads)
	assert.Equal(t, []string{"c1"}, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1, ConfigsDisabled: 1}, res)
}

func TestReconcileD5_NoSubmissionWithLadderingOffIsExempt(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "failed"}, "t1")
	f.global.LadderingEnabled = false

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, 1, f.globalReads)
	assert.Empty(t, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1}, res)
	assert.Equal(t, []string{"t1"}, f.moved, "the tranche still fails")
}

func TestReconcileD5_UnresolvedAndCompletedVerdictsTouchNeitherGlobalNorConfig(t *testing.T) {
	statuses := map[string]string{"t1": "partially_completed", "t2": "scheduled", "t3": "completed", "t4": "failed", "t5": "completed"}
	f := d5Fake(statuses, "t1", "t2", "t3", "t4", "t5")
	f.noProof = map[string]bool{"t4": true}

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Zero(t, f.globalReads)
	assert.Empty(t, f.disabled)
	assert.Equal(t, 0, res.ConfigsDisabled)
	assert.Equal(t, 0, res.Failed)
}

func TestReconcileD5_NilConfigIDHasNothingToDisable(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "failed"}, "t1")
	f.tranches[0].ConfigID = nil

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Zero(t, f.globalReads)
	assert.Empty(t, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Failed: 1}, res)
}

func TestReconcileD5_DisableErrorRollsBackAndCountsErrored(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "canceled"}, "t1")
	boom := errors.New("update failed")
	f.disableErr = map[string]error{"c1": boom}

	res, err := sweep(t, f)

	require.Error(t, err)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Errored: 1}, res)
	require.Len(t, f.txResults, 1)
	require.ErrorIs(t, f.txResults[0], boom)
	assert.Empty(t, f.moved, "the transition is rolled back with the failed disable")
}

func TestReconcileD5_BusyConfigDefersAndStaysFired(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "canceled"}, "t1")
	f.disableErr = map[string]error{"c1": fmt.Errorf("%w: c1", config.ErrLadderConfigBusy)}

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Deferred: 1}, res)
	require.Len(t, f.txResults, 1)
	require.ErrorIs(t, f.txResults[0], config.ErrLadderConfigBusy, "the callback must return the error so the tx rolls back")
	assert.Empty(t, f.moved, "the tranche stays fired")
}

func TestReconcileD5_GlobalReadFailuresNeverDisableOrSkip(t *testing.T) {
	cases := []struct {
		name      string
		global    *config.GlobalConfig
		globalErr error
	}{
		{"read error", nil, errors.New("db down")},
		{"nil config", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := d5Fake(map[string]string{"t1": "failed"}, "t1")
			f.global, f.globalErr = c.global, c.globalErr

			res, err := sweep(t, f)

			require.Error(t, err)
			assert.Equal(t, LadderReconcileResult{Checked: 1, Errored: 1}, res)
			assert.Empty(t, f.disabled)
			assert.Empty(t, f.moved)
		})
	}
}

func TestReconcileD5_LostRaceReadsAndDisablesNothing(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "failed"}, "t1")
	f.notMoved = map[string]bool{"t1": true}

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Zero(t, f.globalReads)
	assert.Empty(t, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 1, LostRace: 1}, res)
}

func TestReconcileD5_SecondTrancheOfADisabledConfigIsNotCounted(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "canceled", "t2": "expired"}, "t1", "t2")
	shared := "c1"
	f.tranches[1].ConfigID = &shared

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, []string{"c1", "c1"}, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 2, Failed: 2, ConfigsDisabled: 1}, res)
}

func TestReconcileD5_GlobalConfigIsReadPerTrancheNotSnapshotted(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "failed", "t2": "failed"}, "t1", "t2")
	f.global = &config.GlobalConfig{LadderExecutionEnabled: true} // laddering off: t1 is exempt.
	f.onGlobalRead = func() {
		if f.globalReads == 1 {
			f.global = switchesOn() // the operator turns the switch on mid-sweep.
		}
	}

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Equal(t, 2, f.globalReads)
	assert.Equal(t, []string{"c2"}, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 2, Failed: 2, ConfigsDisabled: 1}, res)
}

func TestReconcileD5_NoFiredTranchesReadsNothing(t *testing.T) {
	f := d5Fake(nil)

	res, err := sweep(t, f)

	require.NoError(t, err)
	assert.Zero(t, f.globalReads)
	assert.Equal(t, LadderReconcileResult{}, res)
}

func TestReconcileD5_CompletedTrancheNeverDisablesTheConfig(t *testing.T) {
	f := d5Fake(map[string]string{"t1": "completed"}, "t1")

	res, err := ReconcileLadderTranches(context.Background(), f, d5Vis{true}, testNow)

	require.NoError(t, err)
	assert.Equal(t, []string{"t1"}, f.moved, "the tranche completes")
	assert.Zero(t, f.globalReads)
	assert.Empty(t, f.disabled)
	assert.Equal(t, LadderReconcileResult{Checked: 1, Completed: 1}, res)
}
