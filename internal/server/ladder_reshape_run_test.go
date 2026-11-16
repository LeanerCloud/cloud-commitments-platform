package server

import (
	"bytes"
	"context"
	"errors"
	"testing"

	pkgcommon "github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reshapeStore serves only the reads runLadderReshape makes; any other store
// call panics on the nil embedded interface.
type reshapeStore struct {
	config.StoreInterface
	global  *config.GlobalConfig
	accts   map[string]*config.CloudAccount
	runs    map[string]*config.LadderRunDB // by config id; missing = ErrNotFound
	runErr  map[string]error               // by config id
	configs []config.LadderConfigDB
}

func (s *reshapeStore) GetGlobalConfig(context.Context) (*config.GlobalConfig, error) {
	return s.global, nil
}
func (s *reshapeStore) GetLadderConfigs(context.Context) ([]config.LadderConfigDB, error) {
	return s.configs, nil
}
func (s *reshapeStore) GetCloudAccount(_ context.Context, id string) (*config.CloudAccount, error) {
	return s.accts[id], nil
}
func (s *reshapeStore) LatestLadderRunID(_ context.Context, cfgID string) (string, error) {
	if err := s.runErr[cfgID]; err != nil {
		return "", err
	}
	if r, ok := s.runs[cfgID]; ok {
		return r.ID, nil
	}
	return "", config.ErrNotFound
}
func (s *reshapeStore) GetLadderRun(_ context.Context, id string) (*config.LadderRunDB, error) {
	for _, r := range s.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, nil
}

type reshapeFake struct {
	fakeLadderCapability
	errFor   map[string]error // by LadderRunID
	onCall   func()
	scopes   []pkgladder.Scope
	reshapes []pkgladder.BufferReshapeConfig
	summary  pkgladder.ReshapeSummary
	purchase int
}

func (f *reshapeFake) ReshapeBuffer(_ context.Context, sc pkgladder.Scope, cfg pkgladder.BufferReshapeConfig) (pkgladder.ReshapeSummary, error) {
	f.reshapes = append(f.reshapes, cfg)
	f.scopes = append(f.scopes, sc)
	if f.onCall != nil {
		f.onCall()
	}
	return f.summary, f.errFor[*cfg.LadderRunID]
}

func (f *reshapeFake) PurchaseLayer(context.Context, pkgladder.LayerType, pkgcommon.Recommendation, pkgcommon.PurchaseOptions) (pkgcommon.PurchaseResult, error) {
	f.purchase++
	return pkgcommon.PurchaseResult{}, errors.New("PurchaseLayer must never be called by a reshape")
}

// reshapeApp builds n eligible configs (cfg-1..n, runs run-1..n).
func reshapeApp(t *testing.T, n int) (*Application, *reshapeStore, *reshapeFake) {
	t.Helper()
	g, _, _, _ := reshapeFixtures()
	st := &reshapeStore{global: g, accts: map[string]*config.CloudAccount{}, runs: map[string]*config.LadderRunDB{}, runErr: map[string]error{}}
	fake := &reshapeFake{fakeLadderCapability: fakeLadderCapability{t: t}, errFor: map[string]error{}}
	for i := 1; i <= n; i++ {
		_, cfg, acct, run := reshapeFixtures()
		id := string(rune('0' + i))
		cfg.ID, cfg.CloudAccountID, run.ID = "cfg-"+id, "acct-"+id, "run-"+id
		st.configs = append(st.configs, *cfg)
		st.accts["acct-"+id] = acct
		st.runs["cfg-"+id] = run
	}
	app := &Application{Config: st, LadderCapabilityFactory: func(context.Context, string, string) (pkgladder.LadderCapability, error) {
		return fake, nil
	}}
	return app, st, fake
}

func TestRunLadderReshape_OnceNeverPurchases(t *testing.T) {
	app, _, fake := reshapeApp(t, 1)
	res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, ladderReshapeResult{Configs: 1, Reshaped: 1}, res)
	require.Len(t, fake.reshapes, 1)
	assert.Equal(t, "run-1", *fake.reshapes[0].LadderRunID)
	assert.False(t, fake.reshapes[0].DryRun)
	assert.Equal(t, reshapeOwnAcct, fake.scopes[0].AccountID)
	assert.Equal(t, pkgcommon.ProviderAWS, fake.scopes[0].Provider)
	assert.Zero(t, fake.purchase, "a reshape must never call PurchaseLayer")
}

// Two configs on one account would cancel each other's pending exchanges, so
// only the lowest config id reshapes. The own-account gate refuses every other
// account, so the key never splits across accounts within one sweep.
func TestRunLadderReshape_OnePerAccountLowestIDFirst(t *testing.T) {
	app, st, fake := reshapeApp(t, 3)
	st.configs[0], st.configs[2] = st.configs[2], st.configs[0] // store order cfg-3, cfg-2, cfg-1
	res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, ladderReshapeResult{Configs: 3, Refused: 2, Reshaped: 1}, res)
	require.Len(t, fake.reshapes, 1)
	assert.Equal(t, "run-1", *fake.reshapes[0].LadderRunID)
}

func TestRunLadderReshape_IneligibleLowestDoesNotBlockDuplicateKey(t *testing.T) {
	app, st, fake := reshapeApp(t, 2)
	st.configs[0].Enabled = false
	res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, ladderReshapeResult{Configs: 2, Refused: 1, Reshaped: 1}, res)
	require.Len(t, fake.reshapes, 1)
	assert.Equal(t, "run-2", *fake.reshapes[0].LadderRunID)
}

func TestRunLadderReshape_RefusalsNeverCallReshapeBuffer(t *testing.T) {
	cases := map[string]func(*reshapeStore){
		"laddering off":   func(s *reshapeStore) { s.global.LadderingEnabled = false },
		"execution off":   func(s *reshapeStore) { s.global.LadderExecutionEnabled = false },
		"exchange off":    func(s *reshapeStore) { s.global.RIExchangeEnabled = false },
		"4-eyes on":       func(s *reshapeStore) { s.global.RequireDifferentApprover = true },
		"config disabled": func(s *reshapeStore) { s.configs[0].Enabled = false },
		"not aws":         func(s *reshapeStore) { s.configs[0].Provider = "azure" },
		"not auto":        func(s *reshapeStore) { s.configs[0].Mode = "email_approval" },
		"no account":      func(s *reshapeStore) { delete(s.accts, "acct-1") },
		"foreign account": func(s *reshapeStore) { s.accts["acct-1"].ExternalID = "999999999999" },
		"no run":          func(s *reshapeStore) { delete(s.runs, "cfg-1") },
		"run canceled":    func(s *reshapeStore) { s.runs["cfg-1"].Status = pkgladder.RunStatusCanceled },
		"no reshape":      func(s *reshapeStore) { s.runs["cfg-1"].Plan = []byte(`{"actions":[]}`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			app, st, fake := reshapeApp(t, 1)
			mutate(st)
			res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
			require.NoError(t, err)
			assert.Empty(t, fake.reshapes)
			assert.Zero(t, fake.purchase)
			assert.Zero(t, res.Errored, "a refusal is not an error")
		})
	}
}

func TestRunLadderReshape_OtherProcessAccountRefusesAll(t *testing.T) {
	app, _, fake := reshapeApp(t, 2)
	res, err := app.runLadderReshape(context.Background(), "999999999999", "eu-west-1")
	require.NoError(t, err)
	assert.Equal(t, ladderReshapeResult{Configs: 2, Refused: 2}, res)
	assert.Empty(t, fake.reshapes)
}

func TestRunLadderReshape_BadCapsAreErroredNotRun(t *testing.T) {
	app, st, fake := reshapeApp(t, 1)
	st.global.RIExchangeMaxDailyUSD = 0
	res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Errored)
	assert.Empty(t, fake.reshapes)
}

func TestRunLadderReshape_OneConfigErrorDoesNotStopNext(t *testing.T) {
	app, st, fake := reshapeApp(t, 3)
	fake.errFor["run-2"] = errors.New("boom 111111111111")
	fake.summary = pkgladder.ReshapeSummary{Analyzed: 2, Reshaped: 1, Details: []string{"failed: ri-1 account 222222222222"}}
	st.runErr["cfg-1"] = errors.New("db down")
	var out bytes.Buffer
	prev := logging.SetOutput(&out)
	defer logging.SetOutput(prev)

	res, err := app.runLadderReshape(context.Background(), reshapeOwnAcct, "us-east-1")
	require.NoError(t, err)
	assert.Equal(t, ladderReshapeResult{Configs: 3, Refused: 1, Errored: 2}, res)
	require.Len(t, fake.reshapes, 1, "cfg-1 failed its run lookup, cfg-2 ran and failed, cfg-3 is a duplicate of cfg-2")
	assert.Equal(t, "run-2", *fake.reshapes[0].LadderRunID)
	logged := out.String()
	assert.Contains(t, logged, "[ERROR] ladder_reshape: config cfg-2 run run-2: ReshapeBuffer failed")
	assert.Contains(t, logged, "analyzed=2 reshaped=1")
	assert.NotContains(t, logged, "111111111111", "account ids in errors must be redacted")
	assert.NotContains(t, logged, "222222222222", "account ids in details must be redacted")
	assert.Contains(t, logged, "<account-id>")
}

func TestRunLadderReshape_CtxCancelStops(t *testing.T) {
	app, _, fake := reshapeApp(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	fake.onCall = cancel
	res, err := app.runLadderReshape(ctx, reshapeOwnAcct, "us-east-1")
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, fake.reshapes, 1)
	assert.Equal(t, 1, res.Reshaped)
	assert.Equal(t, 1, res.Configs, "no config past the cancel is looked at")

	pre, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	app2, _, fake2 := reshapeApp(t, 1)
	_, err = app2.runLadderReshape(pre, reshapeOwnAcct, "us-east-1")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, fake2.reshapes)
}
