//go:build integration

package api

// Real-Postgres race test for the auto-purchase arming gate. Arming a plan
// checks the grant against the plan's stored accounts; retargeting an armed plan
// checks the grant against the new accounts. Each check reads the other's state
// without a lock, so an arm on [A] and a retarget to [B] can both pass and leave
// an armed plan on B, an account the arming grant does not cover. The gate must
// run under the plan's row lock in both paths.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/database/postgres/testhelpers"
	"github.com/stretchr/testify/require"
)

// slowReadStore widens the check-to-write window: each read the gates depend on
// returns, then waits, so an unlocked gate has time to be overtaken. Under the
// plan lock the wait is harmless.
type slowReadStore struct {
	*config.PostgresStore
}

const armRaceReadDelay = 4 * time.Millisecond

func (s slowReadStore) GetPurchasePlan(ctx context.Context, id string) (*config.PurchasePlan, error) {
	plan, err := s.PostgresStore.GetPurchasePlan(ctx, id)
	time.Sleep(armRaceReadDelay)
	return plan, err
}

func (s slowReadStore) GetPlanAccounts(ctx context.Context, id string) ([]config.CloudAccount, error) {
	accounts, err := s.PostgresStore.GetPlanAccounts(ctx, id)
	time.Sleep(armRaceReadDelay)
	return accounts, err
}

func TestArmAndRetargetNeverLeaveArmedPlanOutsideGrant(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RequireMigratedDatabase(ctx, t, getMigrationsPath())
	pg := config.NewPostgresStore(container.DB)

	newAccount := func(name, ext string) string {
		acct := &config.CloudAccount{Name: name, Provider: "aws", ExternalID: ext, Enabled: true}
		require.NoError(t, pg.CreateCloudAccount(ctx, acct))
		return acct.ID
	}
	acctA := newAccount("race-a", "111111111111")
	acctB := newAccount("race-b", "222222222222")

	onlyA := apsGrant(&auth.PermissionConstraints{AccountIDs: []string{acctA}})
	h, armReq := apgCaller{perms: onlyA}.handlerAndReq(nil, `{"auto_purchase":true}`)
	h.config = slowReadStore{pg}
	retargetBody, err := json.Marshal(map[string][]string{"account_ids": {acctB}})
	require.NoError(t, err)
	retargetReq := *armReq
	retargetReq.Body = string(retargetBody)

	const iterations = 40
	bothWon := 0
	for i := 0; i < iterations; i++ {
		capUSD := 1000.0
		plan := &config.PurchasePlan{
			Name: "arm-retarget-race", Enabled: true, MaxStepCommitment: &capUSD, NotificationDaysBefore: 2,
			Services: map[string]config.ServiceConfig{"aws/rds": {Provider: "aws", Service: "rds", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80}},
			RampSchedule: config.RampSchedule{
				Type: "weekly", PercentPerStep: 25, StepIntervalDays: 7,
				CurrentStep: 0, TotalSteps: 4, StartDate: time.Now().AddDate(0, 0, 1),
			},
		}
		require.NoError(t, pg.CreatePurchasePlan(ctx, plan))
		require.NoError(t, pg.SetPlanAccounts(ctx, plan.ID, []string{acctA}))

		var armErr, retargetErr error
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, armErr = h.patchPlan(ctx, armReq, plan.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, retargetErr = h.setPlanAccounts(ctx, &retargetReq, plan.ID)
		}()
		close(start)
		wg.Wait()

		for _, e := range []error{armErr, retargetErr} {
			if e == nil {
				continue
			}
			ce, ok := IsClientError(e)
			require.True(t, ok, "iteration %d: unexpected error %v", i, e)
			require.Equal(t, http.StatusForbidden, ce.code, "iteration %d: %v", i, e)
		}
		if armErr == nil && retargetErr == nil {
			bothWon++
		}

		stored, err := pg.GetPurchasePlan(ctx, plan.ID)
		require.NoError(t, err)
		accounts, err := pg.GetPlanAccounts(ctx, plan.ID)
		require.NoError(t, err)
		if planArmed(stored) {
			require.Len(t, accounts, 1, "iteration %d", i)
			require.Equal(t, acctA, accounts[0].ID, "iteration %d: armed plan ended up outside the arming grant", i)
		}
	}
	require.Zero(t, bothWon, "arm and retarget must not both succeed")
}
