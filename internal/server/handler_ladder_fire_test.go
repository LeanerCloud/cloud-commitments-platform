package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fireFakeStore implements purchase.LadderFireStore for one enabled config with
// one due, claimable tranche.
type fireFakeStore struct {
	config.StoreInterface
	reads  int
	claims int
}

func (f *fireFakeStore) GetGlobalConfig(context.Context) (*config.GlobalConfig, error) {
	f.reads++
	to := "ops@example.com"
	return &config.GlobalConfig{LadderingEnabled: true, LadderExecutionEnabled: true, PurchaseDelayHours: 48, NotificationEmail: &to}, nil
}

func (f *fireFakeStore) GetLadderConfigs(context.Context) ([]config.LadderConfigDB, error) {
	capUSD := 10.0
	return []config.LadderConfigDB{{ID: "c1", Enabled: true, Provider: "aws", Mode: "auto_approve", MaxHourlyCommitPerRun: &capUSD, CloudAccountID: "acct"}}, nil
}

func (f *fireFakeStore) GetCloudAccount(context.Context, string) (*config.CloudAccount, error) {
	return &config.CloudAccount{ExternalID: "123"}, nil
}

func (f *fireFakeStore) GetLadderSweepCeiling(context.Context, string) (*int64, error) {
	v := int64(6_000_000)
	return &v, nil
}

func (f *fireFakeStore) ListDueLadderTranches(context.Context, string, time.Time, int, []ladder.LayerType) ([]config.LadderDueRow, error) {
	basis, rate := config.LadderAmountCommitment, "1/2"
	return []config.LadderDueRow{{SQLClaimable: true, Tranche: config.LadderTrancheDB{
		ID: "t1", LayerType: ladder.LayerComputeSP, Term: ladder.Term1Year, PaymentOption: ladder.PaymentNoUpfront,
		Status: ladder.TrancheStatusScheduled, AmountUSDHr: 2.5, AmountBasis: &basis, CommitmentRate: &rate,
	}}}, nil
}

func (f *fireFakeStore) ClaimDueLadderTranches(context.Context, string, time.Time, int, config.LadderClaimFilter,
	func(context.Context, pgx.Tx, config.LadderTrancheDB) (string, error),
) ([]config.LadderTrancheDB, error) {
	f.claims++
	return nil, nil
}

// The announce step is not reached here (the fake claims nothing); these only
// make the fake a purchase.LadderFireStore.
func (f *fireFakeStore) SaveExecutionIfStatus(context.Context, *config.PurchaseExecution, []string) error {
	return nil
}

func (f *fireFakeStore) WithTx(context.Context, func(pgx.Tx) error) error { return nil }

func (f *fireFakeStore) LockLadderTrancheChainTx(context.Context, pgx.Tx, string) ([]config.LadderChainNode, error) {
	return nil, nil
}

func (f *fireFakeStore) CancelScheduledExecutionAtomic(context.Context, pgx.Tx, string, *string) (bool, string, error) {
	return false, "", nil
}

func (f *fireFakeStore) TransitionLadderTrancheTx(context.Context, pgx.Tx, string, ladder.TrancheStatus, ladder.TrancheStatus, string) (bool, error) {
	return false, nil
}

func (f *fireFakeStore) SavePurchaseExecutionTx(context.Context, pgx.Tx, *config.PurchaseExecution) error {
	return nil
}

type fakeSender struct{ email.SenderInterface }

func fireApp(store config.StoreInterface, sender email.SenderInterface, dashboard string) *Application {
	return &Application{
		Config: store, Email: sender,
		appConfig:             ApplicationConfig{DashboardURL: dashboard},
		LadderAccountResolver: func(context.Context) (string, string, error) { return "123", "us-east-1", nil },
	}
}

func TestLadderFire_StoreMustBeALadderFireStore(t *testing.T) {
	_, err := fireApp(&ladderTestStore{}, fakeSender{}, "https://d.example.com").handleLadderFire(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot fire ladder tranches")
}

func TestLadderFire_ResolverErrorAbortsBeforeTheStore(t *testing.T) {
	store := &fireFakeStore{}
	app := fireApp(store, fakeSender{}, "https://d.example.com")
	app.LadderAccountResolver = func(context.Context) (string, string, error) { return "", "", errors.New("sts down") }
	_, err := app.handleLadderFire(context.Background())
	require.ErrorContains(t, err, "sts down")
	assert.Zero(t, store.reads)
}

func TestLadderFire_NoRealSenderRefusesTheSweep(t *testing.T) {
	var typedNilSender *email.Sender
	var typedNilNop *email.NopSender
	cases := map[string]email.SenderInterface{
		"nil": nil, "nop": email.NewNopSender(), "typed nil nop": typedNilNop, "typed nil sender": typedNilSender,
	}
	for name, sender := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fireFakeStore{}
			res, err := fireApp(store, sender, "https://d.example.com").handleLadderFire(context.Background())
			require.NoError(t, err)
			assert.Equal(t, 1, res.Skipped)
			assert.Zero(t, store.claims)
		})
	}
}

func TestLadderFire_RealSenderProceedsToTheClaim(t *testing.T) {
	store := &fireFakeStore{}
	res, err := fireApp(store, fakeSender{}, "https://d.example.com/").handleLadderFire(context.Background())
	require.NoError(t, err)
	assert.Zero(t, res.Skipped)
	assert.Equal(t, 1, store.claims)
}

func TestLadderFire_NoDashboardURLRefusesTheSweep(t *testing.T) {
	store := &fireFakeStore{}
	res, err := fireApp(store, fakeSender{}, "/").handleLadderFire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Skipped)
	assert.Zero(t, store.claims)
}

func TestLadderFire_IsNotARegisteredTask(t *testing.T) {
	_, registered := scheduledEventActions["ladder_fire"]
	assert.False(t, registered, "registering ladder_fire is a deliberate later step")
}

func TestLadderFire_OtherProcessAccountRefusesTheSweep(t *testing.T) {
	store := &fireFakeStore{}
	app := fireApp(store, fakeSender{}, "https://d.example.com")
	app.LadderAccountResolver = func(context.Context) (string, string, error) { return "999", "us-east-1", nil }
	res, err := app.handleLadderFire(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Skipped)
	assert.Zero(t, store.claims)
}
