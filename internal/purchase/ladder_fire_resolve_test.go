package purchase

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func dueRow(id string, amount float64, claimable bool) config.LadderDueRow {
	tr := spTranche(amount, ladder.Term1Year)
	tr.ID = id
	return config.LadderDueRow{Tranche: tr, SQLClaimable: claimable}
}

func resolveCfg(capUSD float64) *config.LadderConfigDB {
	return &config.LadderConfigDB{CloudAccountID: "acct-a", MaxHourlyCommitPerRun: &capUSD}
}

func resolveIDs(rows []config.LadderDueRow, cfg *config.LadderConfigDB, region string) ladderResolution {
	return resolveLadderTranches(rows, cfg, NewLadderInstrumentRegistry(), region)
}

func TestResolveLadderTranches_Golden(t *testing.T) {
	rows := []config.LadderDueRow{
		dueRow("T1", 2.5, true), dueRow("T2", 3.0, true), dueRow("T3", 4.0, true),
		dueRow("X-sql", 1.0, false), dueRow("X-cap", 10.5, true),
	}
	res := resolveIDs(rows, resolveCfg(10), "us-east-1")
	assert.Equal(t, []string{"T1", "T2", "T3"}, res.IDs)
	assert.Equal(t, 5, res.Due)
	require.Len(t, res.Unresolved, 2)
	assert.Equal(t, "X-sql", res.Unresolved[0].TrancheID)
	assert.Contains(t, res.Unresolved[0].Reason, "not claimable")
	assert.Equal(t, "X-cap", res.Unresolved[1].TrancheID)
	assert.Equal(t, "above the per-run cap", res.Unresolved[1].Reason)
}

func TestResolveLadderTranches_AmountExactlyAtCapResolves(t *testing.T) {
	res := resolveIDs([]config.LadderDueRow{dueRow("T", 10, true)}, resolveCfg(10), "us-east-1")
	assert.Equal(t, []string{"T"}, res.IDs)
}

func TestResolveLadderTranches_BadAndGoodTranche(t *testing.T) {
	bad := dueRow("bad", 2, true)
	corrupt := "not-a-rate"
	bad.Tranche.CommitmentRate = &corrupt
	res := resolveIDs([]config.LadderDueRow{bad, dueRow("good", 3, true)}, resolveCfg(10), "us-east-1")
	assert.Equal(t, []string{"good"}, res.IDs)
	require.Len(t, res.Unresolved, 1)
	assert.Equal(t, "bad", res.Unresolved[0].TrancheID)
	assert.Contains(t, res.Unresolved[0].Reason, "corrupt tranche")
}

func TestResolveLadderTranches_NoCapRefuses(t *testing.T) {
	res := resolveIDs([]config.LadderDueRow{dueRow("T", 1, true)}, &config.LadderConfigDB{CloudAccountID: "acct-a"}, "us-east-1")
	assert.Empty(t, res.IDs)
	require.Len(t, res.Unresolved, 1)
	assert.Equal(t, "config has no per-run cap", res.Unresolved[0].Reason)
}

func TestResolveLadderTranches_ClaimLimitCaps(t *testing.T) {
	rows := make([]config.LadderDueRow, 0, ladderFireClaimLimit+1)
	for i := 0; i <= ladderFireClaimLimit; i++ {
		rows = append(rows, dueRow(fmt.Sprintf("T%03d", i), 0.1, true))
	}
	res := resolveIDs(rows, resolveCfg(5), "us-east-1")
	assert.Len(t, res.IDs, ladderFireClaimLimit)
	assert.Equal(t, "T000", res.IDs[0])
	assert.True(t, res.Capped)

	exact := resolveIDs(rows[:ladderFireClaimLimit], resolveCfg(10), "us-east-1")
	assert.Len(t, exact.IDs, ladderFireClaimLimit)
	assert.False(t, exact.Capped)
}

func captureLadderLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := logging.SetOutput(&buf)
	t.Cleanup(func() { logging.SetOutput(prev) })
	return &buf
}

func TestLogLadderResolution_AllBuildFailingIsAnError(t *testing.T) {
	buf := captureLadderLog(t)
	rows := []config.LadderDueRow{dueRow("a", 1, true), dueRow("b", 1, true), dueRow("c", 1, true)}
	res := resolveIDs(rows, resolveCfg(10), "") // an empty region fails every Build.
	require.Empty(t, res.IDs)
	logLadderResolution("cfg-1", res)
	out := buf.String()
	assert.Contains(t, out, "ERROR")
	assert.Contains(t, out, "cfg-1")
	assert.Contains(t, out, "due=3 resolved=0")
}

func TestLogLadderResolution_PartialIsNotAnError(t *testing.T) {
	buf := captureLadderLog(t)
	res := resolveIDs([]config.LadderDueRow{dueRow("a", 1, true), dueRow("b", 1, false)}, resolveCfg(10), "us-east-1")
	logLadderResolution("cfg-2", res)
	out := buf.String()
	assert.NotContains(t, out, "ERROR")
	assert.Contains(t, out, "tranche b")
}

func TestLogLadderResolution_NothingDueIsQuiet(t *testing.T) {
	buf := captureLadderLog(t)
	logLadderResolution("cfg-1", ladderResolution{})
	assert.NotContains(t, buf.String(), "ERROR")
}

func TestLogLadderResolution_ListCapWarns(t *testing.T) {
	buf := captureLadderLog(t)
	logLadderResolution("cfg-1", ladderResolution{Due: ladderDueListCap, IDs: []string{"a"}})
	out := buf.String()
	assert.Contains(t, out, "WARN")
	assert.Contains(t, out, "more due rows")
}
