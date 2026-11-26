package api

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func azureSPRows() map[string][]commitmentopts.Combo {
	return map[string][]commitmentopts.Combo{"savingsplans": {
		{Provider: "azure", Service: "savingsplans", TermYears: 1, Payment: "upfront"},
	}}
}

func azureSave(service string) config.ServiceConfig {
	return config.ServiceConfig{Provider: "azure", Service: service, Enabled: true, Term: 3, Payment: "monthly", Coverage: 80}
}

func azureGateHandler(fresh func(context.Context) (map[string][]commitmentopts.Combo, bool, error)) *Handler {
	return &Handler{commitmentOpts: &stubCommitmentOpts{freshFn: fresh}}
}

func freshRows(rows map[string][]commitmentopts.Combo) func(context.Context) (map[string][]commitmentopts.Combo, bool, error) {
	return func(context.Context) (map[string][]commitmentopts.Combo, bool, error) { return rows, true, nil }
}

func unprobed(context.Context) (map[string][]commitmentopts.Combo, bool, error) {
	return nil, false, nil
}

func TestAzureGate_ServiceConfigSave(t *testing.T) {
	cases := map[string]struct {
		fresh   func(context.Context) (map[string][]commitmentopts.Combo, bool, error)
		wantMsg string
		cfg     config.ServiceConfig
		wantErr int
	}{
		"fresh row without the combo blocks, AWS error shape": {
			fresh: freshRows(azureSPRows()), cfg: azureSave("savingsplans"),
			wantErr: 400, wantMsg: "savingsplans does not support 3yr monthly commitments",
		},
		"fresh row with the combo passes": {
			fresh: freshRows(azureSPRows()), cfg: func() config.ServiceConfig {
				c := azureSave("savingsplans")
				c.Term, c.Payment = 1, "upfront"
				return c
			}(),
		},
		"unprobed row passes": {fresh: unprobed, cfg: azureSave("savingsplans")},
		"read error gives 503": {
			fresh: func(context.Context) (map[string][]commitmentopts.Combo, bool, error) {
				return nil, false, errors.New("db down")
			},
			cfg:     azureSave("savingsplans"),
			wantErr: 503, wantMsg: "could not validate",
		},
		"non Savings Plan Azure service is ungated": {
			fresh: func(context.Context) (map[string][]commitmentopts.Combo, bool, error) {
				return nil, false, errors.New("db down")
			},
			cfg: azureSave("compute"),
		},
		"retired spelling is gated": {
			fresh: freshRows(azureSPRows()), cfg: azureSave("savings-plans"),
			wantErr: 400, wantMsg: "does not support 3yr monthly",
		},
		"per plan type slug is gated": {
			fresh: freshRows(azureSPRows()), cfg: azureSave("savings-plans-compute"),
			wantErr: 400, wantMsg: "does not support 3yr monthly",
		},
		"empty payment is not checked": {
			fresh: freshRows(azureSPRows()), cfg: func() config.ServiceConfig { c := azureSave("savingsplans"); c.Payment = ""; return c }(),
		},
		"zero term is not checked": {
			fresh: freshRows(azureSPRows()), cfg: func() config.ServiceConfig { c := azureSave("savingsplans"); c.Term = 0; return c }(),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := azureGateHandler(c.fresh)
			snap := h.prefetchCommitmentOptions(context.Background(), "azure")
			for path, err := range map[string]error{
				"snapshot":    snap.check(c.cfg),
				"import path": h.checkCommitmentOptionCombo(context.Background(), c.cfg),
			} {
				if c.wantErr == 0 {
					require.NoError(t, err, path)
					continue
				}
				ce, ok := IsClientError(err)
				require.True(t, ok, path)
				assert.Equal(t, c.wantErr, ce.code, path)
				assert.Contains(t, ce.message, c.wantMsg, path)
			}
		})
	}
}

// A stale row is reported as not ok by FreshAzureOptions, so it never gates;
// this pins that the gate does not fall back to the stale combos on its own.
func TestAzureGate_StaleRowDoesNotBlock(t *testing.T) {
	h := azureGateHandler(unprobed)
	h.commitmentOpts.(*stubCommitmentOpts).cachedFn = func(context.Context, string) (map[string][]commitmentopts.Combo, bool, error) {
		return azureSPRows(), true, nil
	}
	assert.NoError(t, h.prefetchCommitmentOptions(context.Background(), "azure").check(azureSave("savingsplans")))
}

func TestAzureGate_AWSStillUsesGet(t *testing.T) {
	called := false
	h := &Handler{commitmentOpts: &stubCommitmentOpts{freshFn: func(context.Context) (map[string][]commitmentopts.Combo, bool, error) {
		called = true
		return nil, false, nil
	}, getFn: func(context.Context) (commitmentopts.Options, error) { return nil, commitmentopts.ErrNoData }}}
	assert.NoError(t, h.prefetchCommitmentOptions(context.Background(), "aws").check(config.ServiceConfig{Provider: "aws", Service: "rds", Term: 1, Payment: "all-upfront"}))
	assert.False(t, called)
}
