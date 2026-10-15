package config

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// expectPlanJSONRead scripts the two reads UpdatePurchasePlanTx makes before
// its UPDATE: the FOR UPDATE lock (whose updated_at must equal the caller's
// token) and the stored services/ramp_schedule JSON it merges into.
func expectPlanJSONRead(t *testing.T, mock pgxmock.PgxPoolIface, planID string, updatedAt time.Time) {
	mock.ExpectQuery(`FOR UPDATE`).WithArgs(planID).
		WillReturnRows(rampPlanRows(t, planID, RampSchedule{}, updatedAt, updatedAt, sql.NullTime{}))
	mock.ExpectQuery(`SELECT services,ramp_schedule FROM purchase_plans`).WithArgs(planID).
		WillReturnRows(pgxmock.NewRows([]string{"services", "ramp_schedule"}).AddRow([]byte(`{}`), []byte(`{}`)))
}

func TestUpdatePurchasePlanPreservesTokenOnCommitFailure(t *testing.T) {
	mock := newMock(t)
	store := storeWith(mock)
	readVersion := time.Now().Truncate(time.Microsecond)
	plan := &PurchasePlan{ID: "plan", UpdatedAt: readVersion}
	commitErr := errors.New("commit failed")
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").WithArgs("plan").WillReturnError(pgx.ErrNoRows)
	expectPlanJSONRead(t, mock, "plan", readVersion)
	mock.ExpectQuery(`UPDATE purchase_plans[\s\S]*WHERE id = \$1 AND updated_at = \$8[\s\S]*RETURNING updated_at`).
		WithArgs(anyArgsCfg(12)...).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at"}).AddRow(readVersion.Add(time.Second)))
	mock.ExpectCommit().WillReturnError(commitErr)
	mock.ExpectRollback().WillReturnError(pgx.ErrTxClosed)
	require.ErrorIs(t, store.UpdatePurchasePlan(context.Background(), plan), commitErr)
	require.Equal(t, readVersion, plan.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpdatePurchasePlanPropagatesConflictProbeFailure(t *testing.T) {
	mock := newMock(t)
	store := storeWith(mock)
	plan := &PurchasePlan{ID: "plan", UpdatedAt: time.Now()}
	readVersion := plan.UpdatedAt
	probeErr := errors.New("probe failed")
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").WithArgs(plan.ID).WillReturnError(pgx.ErrNoRows)
	expectPlanJSONRead(t, mock, plan.ID, readVersion)
	mock.ExpectQuery("UPDATE purchase_plans").WithArgs(anyArgsCfg(12)...).
		WillReturnRows(pgxmock.NewRows([]string{"updated_at"}))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(plan.ID).WillReturnError(probeErr)
	mock.ExpectRollback()
	err := store.UpdatePurchasePlan(context.Background(), plan)
	require.ErrorIs(t, err, probeErr)
	require.NotErrorIs(t, err, ErrPurchasePlanConflict)
	require.NotErrorIs(t, err, ErrNotFound)
	require.Equal(t, readVersion, plan.UpdatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}
