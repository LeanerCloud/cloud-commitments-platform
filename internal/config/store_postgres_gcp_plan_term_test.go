package config

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcpPlanTermRow is a stored global_config row (globalConfigCols order) whose
// only interesting value is experimental_gcp_plan_term.
func gcpPlanTermRow(planTerm bool) *pgxmock.Rows {
	return pgxmock.NewRows(globalConfigCols).AddRow(
		[]string{"gcp"}, (*string)(nil), true,
		3, "no-upfront", 80.0, RampImmediate,
		false, "manual", 95.0,
		0.0, 0.0, 30,
		true, "daily", 3,
		"{}",
		24, 7,
		0,
		false,
		false,
		"convertible",
		false,
		planTerm, // experimental_gcp_plan_term
		false,
		nil, nil, nil,
		nil, nil, nil,
	)
}

// The experimental GCP plan-term switch is off until an operator opts in
// (platform issue #858): no stored row and a fresh row both read false.
func TestPGXMock_GetGlobalConfig_ExperimentalGCPPlanTermDefaultsOff(t *testing.T) {
	t.Run("no row", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery("SELECT").WillReturnError(pgx.ErrNoRows)
		cfg, err := storeWith(mock).GetGlobalConfig(context.Background())
		require.NoError(t, err)
		assert.False(t, cfg.ExperimentalGCPPlanTerm)
	})
	t.Run("stored false", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery("SELECT").WillReturnRows(gcpPlanTermRow(false))
		cfg, err := storeWith(mock).GetGlobalConfig(context.Background())
		require.NoError(t, err)
		assert.False(t, cfg.ExperimentalGCPPlanTerm)
	})
	t.Run("stored true", func(t *testing.T) {
		mock := newMock(t)
		mock.ExpectQuery("SELECT").WillReturnRows(gcpPlanTermRow(true))
		cfg, err := storeWith(mock).GetGlobalConfig(context.Background())
		require.NoError(t, err)
		assert.True(t, cfg.ExperimentalGCPPlanTerm)
	})
}

// SaveGlobalConfig must bind the switch as $25 so a saved true is not lost.
func TestPGXMock_SaveGlobalConfig_BindsExperimentalGCPPlanTerm(t *testing.T) {
	for _, want := range []bool{false, true} {
		mock := newMock(t)
		args := anyArgsCfg(32)
		args[24] = want
		mock.ExpectExec("INSERT INTO global_config").WithArgs(args...).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))

		err := storeWith(mock).SaveGlobalConfig(context.Background(), &GlobalConfig{ExperimentalGCPPlanTerm: want})
		require.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet(), "want=%v", want)
	}
}
