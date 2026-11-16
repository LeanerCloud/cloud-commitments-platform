package server

import (
	"math"
	"testing"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reshapeOwnAcct = "123456789012"

func reshapeFixtures() (*config.GlobalConfig, *config.LadderConfigDB, *config.CloudAccount, *config.LadderRunDB) {
	g := &config.GlobalConfig{
		LadderingEnabled:            true,
		LadderExecutionEnabled:      true,
		RIExchangeEnabled:           true,
		RIExchangeMaxPerExchangeUSD: 100,
		RIExchangeMaxDailyUSD:       500,
	}
	cfg := &config.LadderConfigDB{
		ID: "cfg-1", Enabled: true, Provider: "aws", Mode: "auto_approve",
		BufferUtilizationThreshold: 70, LookbackDays: 30,
	}
	acct := &config.CloudAccount{ExternalID: reshapeOwnAcct}
	run := &config.LadderRunDB{
		ID:     "run-1",
		Status: pkgladder.RunStatusPlanned,
		Plan:   []byte(`{"actions":[{"action":"purchase","layer":"compute_sp"},{"action":"reshape","layer":"convertible_ri"}]}`),
	}
	return g, cfg, acct, run
}

func TestLadderReshapeEligible(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*config.GlobalConfig, *config.LadderConfigDB, **config.CloudAccount, *string, **config.LadderRunDB)
		want   ladderReshapeRefusal
	}{
		{"happy path", func(*config.GlobalConfig, *config.LadderConfigDB, **config.CloudAccount, *string, **config.LadderRunDB) {
		}, ladderReshapeOK},
		{"laddering off", func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			g.LadderingEnabled = false
		}, reshapeRefuseLadderingOff},
		{"execution off", func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			g.LadderExecutionEnabled = false
		}, reshapeRefuseExecutionOff},
		{"exchange off", func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			g.RIExchangeEnabled = false
		}, reshapeRefuseExchangeOff},
		{"4-eyes on", func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			g.RequireDifferentApprover = true
		}, reshapeRefuseFourEyes},
		{"config disabled", func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			c.Enabled = false
		}, reshapeRefuseConfigDisabled},
		{"wrong provider", func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			c.Provider = "azure"
		}, reshapeRefuseProvider},
		{"email approval mode", func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			c.Mode = "email_approval"
		}, reshapeRefuseMode},
		{"no account", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, a **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			*a = nil
		}, reshapeRefuseNoAccount},
		{"own account unknown", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, own *string, _ **config.LadderRunDB) {
			*own = ""
		}, reshapeRefuseOwnAccountUnknown},
		{"foreign account", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, a **config.CloudAccount, _ *string, _ **config.LadderRunDB) {
			(*a).ExternalID = "999999999999"
		}, reshapeRefuseForeignAccount},
		{"no run", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, r **config.LadderRunDB) {
			*r = nil
		}, reshapeRefuseNoRun},
		{"run without id", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, r **config.LadderRunDB) {
			(*r).ID = ""
		}, reshapeRefuseNoRun},
		{"plan without reshape", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, r **config.LadderRunDB) {
			(*r).Plan = []byte(`{"actions":[{"action":"purchase"},{"action":"hold"}]}`)
		}, reshapeRefuseNoReshapeAction},
		{"empty plan", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, r **config.LadderRunDB) {
			(*r).Plan = nil
		}, reshapeRefuseNoReshapeAction},
		{"undecodable plan", func(_ *config.GlobalConfig, _ *config.LadderConfigDB, _ **config.CloudAccount, _ *string, r **config.LadderRunDB) {
			(*r).Plan = []byte(`{not json`)
		}, reshapeRefuseNoReshapeAction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, cfg, acct, run := reshapeFixtures()
			own := reshapeOwnAcct
			tc.mutate(g, cfg, &acct, &own, &run)
			assert.Equal(t, tc.want, ladderReshapeEligible(g, cfg, acct, own, run))
		})
	}

	t.Run("run status allow-list", func(t *testing.T) {
		statuses := map[string]ladderReshapeRefusal{
			"planned": ladderReshapeOK, "approved": ladderReshapeOK,
			"executing": ladderReshapeOK, "completed": ladderReshapeOK,
			"awaiting_approval": reshapeRefuseRunStatus, "failed": reshapeRefuseRunStatus,
			"expired": reshapeRefuseRunStatus, "canceled": reshapeRefuseRunStatus,
			"cancel" + "led": reshapeRefuseRunStatus, "bogus": reshapeRefuseRunStatus, "": reshapeRefuseRunStatus,
		}
		for st, want := range statuses {
			g, cfg, acct, run := reshapeFixtures()
			run.Status = pkgladder.RunStatus(st)
			assert.Equal(t, want, ladderReshapeEligible(g, cfg, acct, reshapeOwnAcct, run), "status %q", st)
		}
	})
	t.Run("nil global", func(t *testing.T) {
		_, cfg, acct, run := reshapeFixtures()
		assert.Equal(t, reshapeRefuseNoGlobal, ladderReshapeEligible(nil, cfg, acct, reshapeOwnAcct, run))
	})
	t.Run("nil config", func(t *testing.T) {
		g, _, acct, run := reshapeFixtures()
		assert.Equal(t, reshapeRefuseNoConfig, ladderReshapeEligible(g, nil, acct, reshapeOwnAcct, run))
	})
}

func TestLadderReshapeConfig_Happy(t *testing.T) {
	g, cfg, _, _ := reshapeFixtures()
	got, err := ladderReshapeConfig(g, cfg, "run-1")
	require.NoError(t, err)
	require.NotNil(t, got.LadderRunID, "a nil run id would cancel the standalone task's pendings")
	assert.Equal(t, "run-1", *got.LadderRunID)
	require.NotNil(t, got.MaxPaymentPerExchangeUSD)
	require.NotNil(t, got.MaxPaymentDailyUSD)
	assert.InDelta(t, 100.0, *got.MaxPaymentPerExchangeUSD, 0)
	assert.InDelta(t, 500.0, *got.MaxPaymentDailyUSD, 0)
	assert.InDelta(t, 70.0, got.UtilizationThresholdPct, 0)
	assert.Equal(t, 30, got.LookbackDays)
	assert.False(t, got.DryRun)
}

func TestLadderReshapeConfig_Refusals(t *testing.T) {
	cases := []struct {
		mutate func(*config.GlobalConfig, *config.LadderConfigDB, *string)
		name   string
	}{
		{mutate: func(_ *config.GlobalConfig, _ *config.LadderConfigDB, id *string) { *id = "" }, name: "empty run id"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) { g.RIExchangeMaxPerExchangeUSD = 0 }, name: "zero per-exchange cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) { g.RIExchangeMaxPerExchangeUSD = -1 }, name: "negative per-exchange cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) {
			g.RIExchangeMaxPerExchangeUSD = math.NaN()
		}, name: "NaN per-exchange cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) {
			g.RIExchangeMaxPerExchangeUSD = math.Inf(1)
		}, name: "Inf per-exchange cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) { g.RIExchangeMaxDailyUSD = 0 }, name: "zero daily cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) { g.RIExchangeMaxDailyUSD = -5 }, name: "negative daily cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) {
			g.RIExchangeMaxDailyUSD = math.NaN()
		}, name: "NaN daily cap"},
		{mutate: func(g *config.GlobalConfig, _ *config.LadderConfigDB, _ *string) {
			g.RIExchangeMaxDailyUSD = math.Inf(1)
		}, name: "Inf daily cap"},
		{mutate: func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ *string) { c.BufferUtilizationThreshold = 0 }, name: "zero threshold"},
		{mutate: func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ *string) { c.BufferUtilizationThreshold = 101 }, name: "threshold over 100"},
		{mutate: func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ *string) {
			c.BufferUtilizationThreshold = math.NaN()
		}, name: "NaN threshold"},
		{mutate: func(_ *config.GlobalConfig, c *config.LadderConfigDB, _ *string) { c.LookbackDays = 0 }, name: "zero lookback"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, cfg, _, _ := reshapeFixtures()
			id := "run-1"
			tc.mutate(g, cfg, &id)
			_, err := ladderReshapeConfig(g, cfg, id)
			require.Error(t, err)
		})
	}
	t.Run("nil inputs", func(t *testing.T) {
		g, cfg, _, _ := reshapeFixtures()
		_, err := ladderReshapeConfig(nil, cfg, "r")
		require.Error(t, err)
		_, err = ladderReshapeConfig(g, nil, "r")
		require.Error(t, err)
	})
}
