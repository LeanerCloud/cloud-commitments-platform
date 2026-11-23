//go:build integration

package config

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/stretchr/testify/require"
)

// A saved ladder run must read back with every column in its own field. Mode
// and Cadence are both nullable text, so a swapped scan target type-checks and
// only distinct values expose it; the monetary and actor columns get distinct
// values for the same reason.
func TestLadderRun_RoundTripEveryColumn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store := setupLadderStore(ctx, t)
	cfgID := seedLadderConfig(ctx, t, store)

	started := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	completed := started.Add(time.Hour)
	expires := started.Add(48 * time.Hour)
	fireAt := started.Add(24 * time.Hour)
	mode := string(ladder.ModeEmailApproval)
	cadence := string(ladder.CadenceWeekly)
	hash, approvedBy, canceledBy := "hash-1", "approver-1", "canceler-1"
	saved, err := store.SaveLadderRun(ctx, &LadderRunDB{
		ConfigID: &cfgID, StartedAt: started, CompletedAt: &completed,
		Status: ladder.RunStatusAwaitingApproval, Mode: &mode, Cadence: &cadence,
		BaselineUSDHr: fptr(11.5), TargetUSDHr: fptr(12.5), ExistingUSDHr: fptr(13.5), GapUSDHr: fptr(14.5),
		Plan:              []byte(`{"k":"v"}`),
		TotalHourlyCommit: 21.25, TotalUpfrontCost: 22.25, EstimatedSavings: 23.25,
		ApprovalTokenHash: &hash, ApprovalTokenExpiresAt: &expires,
		ApprovedBy: &approvedBy, CancelledBy: &canceledBy, FireAt: &fireAt,
	})
	require.NoError(t, err)

	got, err := store.GetLadderRun(ctx, saved.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	require.Equal(t, saved.ID, got.ID)
	require.Equal(t, cfgID, *got.ConfigID)
	require.True(t, started.Equal(got.StartedAt))
	require.True(t, completed.Equal(*got.CompletedAt))
	require.Equal(t, ladder.RunStatusAwaitingApproval, got.Status)
	require.Equal(t, "email_approval", *got.Mode)
	require.Equal(t, "weekly", *got.Cadence)
	require.Equal(t, 11.5, *got.BaselineUSDHr)
	require.Equal(t, 12.5, *got.TargetUSDHr)
	require.Equal(t, 13.5, *got.ExistingUSDHr)
	require.Equal(t, 14.5, *got.GapUSDHr)
	require.JSONEq(t, `{"k":"v"}`, string(got.Plan))
	require.Equal(t, 21.25, got.TotalHourlyCommit)
	require.Equal(t, 22.25, got.TotalUpfrontCost)
	require.Equal(t, 23.25, got.EstimatedSavings)
	require.Equal(t, "hash-1", *got.ApprovalTokenHash)
	require.True(t, expires.Equal(*got.ApprovalTokenExpiresAt))
	require.Equal(t, "approver-1", *got.ApprovedBy)
	require.Equal(t, "canceler-1", *got.CancelledBy)
	require.True(t, fireAt.Equal(*got.FireAt))
}
