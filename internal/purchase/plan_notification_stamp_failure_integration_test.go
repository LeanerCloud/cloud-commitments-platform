//go:build integration

package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type c3aRawState struct {
	plan, executions, accounts, suppressions, history    string
	planExceptStamp, planExceptClaim, rootExceptRotation string
	planUpdated, rootUpdated                             time.Time
}

func c3aReadState(f *cumFixture) c3aRawState {
	f.t.Helper()
	var state c3aRawState
	err := f.pool.QueryRow(f.ctx, `
		WITH p AS (SELECT to_jsonb(p) AS row FROM purchase_plans p WHERE id=$1),
		e AS (SELECT * FROM purchase_executions WHERE plan_id=$1),
		a AS (SELECT to_jsonb(pa) AS link, to_jsonb(ca) AS account
		      FROM plan_accounts pa JOIN cloud_accounts ca ON ca.id=pa.account_id WHERE pa.plan_id=$1),
		s AS (SELECT s.* FROM purchase_suppressions s JOIN e ON e.execution_id=s.execution_id)
		SELECT p.row::text,
		COALESCE((SELECT jsonb_agg(to_jsonb(e) ORDER BY execution_id) FROM e),'[]')::text,
		COALESCE((SELECT jsonb_agg(jsonb_build_object('link',link,'account',account) ORDER BY link::text) FROM a),'[]')::text,
		COALESCE((SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM s),'[]')::text,
		COALESCE((SELECT jsonb_agg(to_jsonb(e) ORDER BY execution_id) FROM e WHERE step_number=5),'[]')::text,
		(p.row-'last_notification_sent'-'updated_at')::text,
		(p.row-'last_notification_sent'-'updated_at'-'ramp_baseline'-'ramp_ledger_state')::text,
		COALESCE((SELECT jsonb_agg(to_jsonb(e)-'approval_token_hash'-'approval_token_expires_at'-'updated_at'
		ORDER BY execution_id) FROM e WHERE step_number=1),'[]')::text,
		(p.row->>'updated_at')::timestamptz,
		COALESCE((SELECT updated_at FROM e WHERE step_number=1), 'epoch'::timestamptz) FROM p`, f.planID).
		Scan(&state.plan, &state.executions, &state.accounts, &state.suppressions, &state.history,
			&state.planExceptStamp, &state.planExceptClaim, &state.rootExceptRotation,
			&state.planUpdated, &state.rootUpdated)
	require.NoError(f.t, err)
	state.planUpdated, state.rootUpdated = state.planUpdated.UTC(), state.rootUpdated.UTC()
	return state
}

type c3aStampStore struct {
	*config.PostgresStore
	fixture *cumFixture
	before  []c3aRawState
	errors  []error
}

func (s *c3aStampStore) StampPlanNotificationSent(ctx context.Context, id string, when time.Time) error {
	s.before = append(s.before, c3aReadState(s.fixture))
	err := s.PostgresStore.StampPlanNotificationSent(ctx, id, when)
	s.errors = append(s.errors, err)
	return err
}

type c3aDelivery struct {
	plan, root, date, hash string
	recommendations        []email.RecommendationSummary
}

type c3aSender struct {
	MockEmailSender
	deliveries []c3aDelivery
}

func (s *c3aSender) SendScheduledPurchaseNotification(_ context.Context, data email.NotificationData) error {
	s.deliveries = append(s.deliveries, c3aDelivery{plan: data.PlanID, root: data.ExecutionID,
		date: data.PurchaseDate, hash: config.HashApprovalToken(data.ApprovalToken),
		recommendations: append([]email.RecommendationSummary(nil), data.Recommendations...)})
	return nil
}

type c3aResolver struct {
	stepResolver
	calls int
}

func (r *c3aResolver) Resolve(ctx context.Context, p *config.PurchasePlan, ids []string, step int,
	base config.PlanRampBaseline, held map[string]config.PlanRampQuantity) (planstep.Result, error) {
	r.calls++
	return r.stepResolver.Resolve(ctx, p, ids, step, base, held)
}

func TestC3aPlanNotificationStampBoundary(t *testing.T) {
	for _, name := range []string{"healthy", "postgres_stamp_refusal"} {
		t.Run(name, func(t *testing.T) {
			f := newCumFixture(t, 100)
			f.src.shown = func() []config.RecommendationRecord {
				return []config.RecommendationRecord{cumRec(f.account, "m5.t0", 100)}
			}
			observer := &c3aStampStore{PostgresStore: f.store, fixture: f}
			sender := &c3aSender{}
			resolver := &c3aResolver{stepResolver: f.m.resolver}
			f.m.config, f.m.email, f.m.resolver = observer, sender, resolver
			initialPlan := f.plan()
			require.True(t, initialPlan.Enabled && initialPlan.AutoPurchase)
			require.False(t, initialPlan.RampSchedule.DatesManaged)
			require.Equal(t, 0, initialPlan.RampSchedule.CurrentStep)
			require.Equal(t, 30, initialPlan.NotificationDaysBefore)
			require.NotNil(t, initialPlan.NextExecutionDate)
			savedDate := *initialPlan.NextExecutionDate
			require.Nil(t, initialPlan.LastNotificationSent)
			global, err := f.store.GetGlobalConfig(f.ctx)
			require.NoError(t, err)
			require.True(t, global.AutoPurchaseEnabled)
			account, err := f.store.GetCloudAccount(f.ctx, f.account)
			require.NoError(t, err)
			require.True(t, account.Enabled)
			current := cumRec(f.account, "m5.t0", 100)
			currentKey, err := config.RecIdentityKey(&current)
			require.NoError(t, err)
			historical := cumRec(f.account, "m5.history", 3)
			historical.Selected, historical.Purchased = true, true
			historyKey, err := config.RecIdentityKey(&historical)
			require.NoError(t, err)
			require.NotEqual(t, currentKey, historyKey)
			require.NoError(t, f.store.SavePurchaseExecution(f.ctx, &config.PurchaseExecution{
				PlanID: f.planID, ExecutionID: uuid.NewString(), StepNumber: 5, Status: "completed",
				ScheduledDate: savedDate.Add(-7 * 24 * time.Hour), Recommendations: []config.RecommendationRecord{historical},
			}))
			var baseline config.PlanRampBaseline
			var committed map[string]config.PlanRampQuantity
			require.NoError(t, f.store.WithTx(f.ctx, func(tx pgx.Tx) error {
				var readErr error
				baseline, readErr = f.store.GetPlanRampBaselineTx(f.ctx, tx, f.planID)
				if readErr != nil {
					return readErr
				}
				committed, readErr = f.store.ListPlanCommittedTx(f.ctx, tx, f.planID)
				return readErr
			}))
			require.Nil(t, baseline)
			require.Equal(t, map[string]config.PlanRampQuantity{historyKey: {Count: 3}}, committed)
			require.Empty(t, f.stepRows(1))
			var ledger string
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT ramp_ledger_state FROM purchase_plans WHERE id=$1`, f.planID).Scan(&ledger))
			require.Equal(t, "new", ledger)
			before := c3aReadState(f)
			require.NotEqual(t, "[]", before.accounts)
			require.NotEqual(t, "[]", before.history)
			require.Equal(t, "[]", before.suppressions)
			var trigger string
			if name == "postgres_stamp_refusal" {
				owned := "c3a_stamp_" + uuid.NewString()
				function := pgx.Identifier{owned + "_fn"}.Sanitize()
				trigger = pgx.Identifier{owned + "_trigger"}.Sanitize()
				_, err = f.pool.Exec(f.ctx, `CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $$
					BEGIN
					IF NEW.last_notification_sent IS DISTINCT FROM OLD.last_notification_sent THEN
					RAISE EXCEPTION USING ERRCODE='P0001', MESSAGE='c3a synthetic stamp refusal';
					END IF;
					RETURN NEW; END $$`)
				require.NoError(t, err)
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_, cleanupErr := f.pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+trigger+` ON purchase_plans`)
					if cleanupErr != nil {
						t.Error("owned stamp trigger cleanup failed")
					}
					_, cleanupErr = f.pool.Exec(ctx, `DROP FUNCTION `+function+`() `)
					require.NoError(t, cleanupErr)
				})
				_, err = f.pool.Exec(f.ctx, `CREATE TRIGGER `+trigger+` BEFORE UPDATE OF last_notification_sent ON purchase_plans
				FOR EACH ROW WHEN (NEW.last_notification_sent IS DISTINCT FROM OLD.last_notification_sent) EXECUTE FUNCTION `+function+`() `)
				require.NoError(t, err)
			}
			start := time.Now()
			result, outerErr := f.m.SendUpcomingPurchaseNotifications(f.ctx)
			end := time.Now()
			after := c3aReadState(f)
			require.Len(t, observer.errors, 1)
			require.Len(t, observer.before, 1)
			require.Len(t, sender.deliveries, 1)
			require.Equal(t, 1, resolver.calls)
			require.True(t, before.history == observer.before[0].history, "first-tick historical raw-row equality")
			require.True(t, before.history == after.history, "first-tick post-action historical raw-row equality")
			require.True(t, before.accounts == after.accounts)
			require.True(t, before.planExceptClaim == after.planExceptClaim)
			require.True(t, savedDate.Sub(end) > 12*time.Hour && savedDate.Sub(start) < 36*time.Hour)
			if trigger != "" {
				var pgErr *pgconn.PgError
				require.True(t, errors.As(observer.errors[0], &pgErr))
				require.Equal(t, "P0001", pgErr.Code)
				require.Equal(t, "c3a synthetic stamp refusal", pgErr.Message)
			}
			require.NoError(t, outerErr)
			require.NotNil(t, result)
			if trigger != "" {
				require.Equal(t, 0, result.Notified, "outer-result-after-pg-stamp-refusal")
				require.True(t, observer.before[0] == after, "failed stamp preserved full pre-stamp raw state")
				require.Nil(t, f.plan().LastNotificationSent)
			} else {
				require.NoError(t, observer.errors[0])
				require.Equal(t, 1, result.Notified)
				stamp := f.plan().LastNotificationSent
				require.NotNil(t, stamp)
				require.False(t, stamp.Before(start.Truncate(time.Microsecond)))
				require.False(t, stamp.After(end.Add(time.Microsecond)))
				require.True(t, observer.before[0].executions == after.executions)
				require.True(t, observer.before[0].suppressions == after.suppressions)
				require.True(t, observer.before[0].planExceptStamp == after.planExceptStamp)
				require.False(t, after.planUpdated.Before(start.Truncate(time.Microsecond)))
				require.False(t, after.planUpdated.After(end.Add(time.Microsecond)))
			}
			rows := f.stepRows(1)
			require.Len(t, rows, 1)
			root := rows[0]
			require.Equal(t, f.planID, root.PlanID)
			require.Equal(t, 1, root.StepNumber)
			require.Equal(t, "notified", root.Status)
			require.Nil(t, root.CloudAccountID)
			require.Nil(t, root.RetryExecutionID)
			require.Zero(t, root.RetryAttemptN)
			require.Len(t, root.Recommendations, 1)
			require.Equal(t, 20, totalCount(&root))
			require.True(t, root.Recommendations[0].Selected)
			require.False(t, root.Recommendations[0].Purchased)
			require.NotNil(t, root.Recommendations[0].CloudAccountID)
			require.Equal(t, f.account, *root.Recommendations[0].CloudAccountID)
			require.Equal(t, sender.deliveries[0].hash, root.ApprovalToken)
			require.NotNil(t, root.ApprovalTokenExpiresAt)
			require.False(t, root.ApprovalTokenExpiresAt.Before(start.Add(config.ApprovalTokenTTL).Truncate(time.Microsecond)))
			require.False(t, root.ApprovalTokenExpiresAt.After(end.Add(config.ApprovalTokenTTL+time.Microsecond)))
			require.False(t, root.ScheduledDate.Before(start.AddDate(0, 0, 30).Truncate(time.Microsecond)))
			require.False(t, root.ScheduledDate.After(end.AddDate(0, 0, 30).Add(time.Microsecond)))
			require.NotEqual(t, savedDate, root.ScheduledDate)
			require.Equal(t, f.planID, sender.deliveries[0].plan)
			require.Equal(t, root.ExecutionID, sender.deliveries[0].root)
			require.Equal(t, root.ScheduledDate.Format("January 2, 2006"), sender.deliveries[0].date)
			require.Len(t, sender.deliveries[0].recommendations, 1)
			require.Equal(t, 20, sender.deliveries[0].recommendations[0].Count)
			baseline, err = f.baseline()
			require.NoError(t, err)
			require.Equal(t, config.PlanRampBaseline{currentKey: {Count: 100}}, baseline)
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT ramp_ledger_state FROM purchase_plans WHERE id=$1`, f.planID).Scan(&ledger))
			require.Equal(t, "trusted", ledger)
			require.True(t, before.plan != after.plan)
			require.False(t, after.planUpdated.Before(before.planUpdated))
			require.Equal(t, 0, f.plan().RampSchedule.CurrentStep)
			require.NotNil(t, f.plan().NextExecutionDate)
			require.True(t, savedDate.Equal(*f.plan().NextExecutionDate))
			var suppressionCount, executionCount int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM purchase_executions WHERE plan_id=$1`, f.planID).Scan(&executionCount))
			require.Equal(t, 2, executionCount)
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT COALESCE(sum(suppressed_count),0) FROM purchase_suppressions
			WHERE execution_id=$1 AND account_id=$2 AND provider='aws' AND service='ec2' AND region='us-east-1' AND resource_type='m5.t0'`, root.ExecutionID, f.account).Scan(&suppressionCount))
			require.Equal(t, 20, suppressionCount)
			var suppressionRows int
			require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM purchase_suppressions`).Scan(&suppressionRows))
			require.Equal(t, 1, suppressionRows)
			require.NotEqual(t, "[]", after.suppressions)
			require.NoError(t, f.store.WithTx(f.ctx, func(tx pgx.Tx) error {
				var readErr error
				committed, readErr = f.store.ListPlanCommittedTx(f.ctx, tx, f.planID)
				return readErr
			}))
			require.Equal(t, map[string]config.PlanRampQuantity{historyKey: {Count: 3}, currentKey: {Count: 20}}, committed)
			if trigger != "" {
				_, err = f.pool.Exec(f.ctx, `ALTER TABLE purchase_plans DISABLE TRIGGER `+trigger)
				require.NoError(t, err)
				retryBefore := c3aReadState(f)
				require.True(t, retryBefore == after)
				retryStart := time.Now()
				result, err = f.m.SendUpcomingPurchaseNotifications(f.ctx)
				retryEnd := time.Now()
				retryAfter := c3aReadState(f)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, 1, result.Notified)
				require.Len(t, sender.deliveries, 2)
				require.Len(t, observer.errors, 2)
				require.NoError(t, observer.errors[1])
				require.Equal(t, 1, resolver.calls)
				require.True(t, savedDate.Sub(retryEnd) > 12*time.Hour)
				require.True(t, retryBefore.history == observer.before[1].history && retryBefore.history == retryAfter.history)
				require.True(t, retryBefore.accounts == retryAfter.accounts)
				require.True(t, retryBefore.suppressions == retryAfter.suppressions)
				require.True(t, retryBefore.planExceptStamp == retryAfter.planExceptStamp)
				require.True(t, retryBefore.rootExceptRotation == retryAfter.rootExceptRotation)
				require.True(t, observer.before[1].executions == retryAfter.executions)
				require.True(t, observer.before[1].suppressions == retryAfter.suppressions)
				require.True(t, observer.before[1].accounts == retryAfter.accounts)
				require.True(t, observer.before[1].planExceptStamp == retryAfter.planExceptStamp)
				require.False(t, retryAfter.rootUpdated.Before(retryStart.Truncate(time.Microsecond)))
				require.False(t, retryAfter.rootUpdated.After(retryEnd.Add(time.Microsecond)))
				require.False(t, retryAfter.rootUpdated.Before(retryBefore.rootUpdated))
				require.False(t, retryAfter.planUpdated.Before(retryStart.Truncate(time.Microsecond)))
				require.False(t, retryAfter.planUpdated.After(retryEnd.Add(time.Microsecond)))
				retryRows := f.stepRows(1)
				require.Len(t, retryRows, 1)
				require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM purchase_executions WHERE plan_id=$1`, f.planID).Scan(&executionCount))
				require.Equal(t, 2, executionCount)
				require.Equal(t, root.ExecutionID, retryRows[0].ExecutionID)
				require.NotEqual(t, sender.deliveries[0].hash, sender.deliveries[1].hash)
				require.Equal(t, sender.deliveries[1].hash, retryRows[0].ApprovalToken)
				require.Equal(t, root.ExecutionID, sender.deliveries[1].root)
				require.Equal(t, f.planID, sender.deliveries[1].plan)
				require.Equal(t, sender.deliveries[0].date, sender.deliveries[1].date)
				require.Equal(t, sender.deliveries[0].recommendations, sender.deliveries[1].recommendations)
				require.NotNil(t, retryRows[0].ApprovalTokenExpiresAt)
				require.False(t, retryRows[0].ApprovalTokenExpiresAt.Before(retryStart.Add(config.ApprovalTokenTTL).Truncate(time.Microsecond)))
				require.False(t, retryRows[0].ApprovalTokenExpiresAt.After(retryEnd.Add(config.ApprovalTokenTTL+time.Microsecond)))
				require.False(t, retryRows[0].ApprovalTokenExpiresAt.Before(*root.ApprovalTokenExpiresAt))
				stamp := f.plan().LastNotificationSent
				require.NotNil(t, stamp)
				require.False(t, stamp.Before(retryStart.Truncate(time.Microsecond)))
				require.False(t, stamp.After(retryEnd.Add(time.Microsecond)))
				after = retryAfter
			}
			deliveries, stamps := len(sender.deliveries), len(observer.errors)
			cooldownBefore := c3aReadState(f)
			require.True(t, cooldownBefore == after)
			cooldownStart := time.Now()
			result, err = f.m.SendUpcomingPurchaseNotifications(f.ctx)
			cooldownEnd := time.Now()
			cooldownAfter := c3aReadState(f)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Zero(t, result.Notified)
			require.Len(t, sender.deliveries, deliveries)
			require.Len(t, observer.errors, stamps)
			require.Equal(t, 1, resolver.calls)
			require.True(t, cooldownBefore == cooldownAfter, "cooldown full raw-state equality")
			require.True(t, savedDate.Sub(cooldownEnd) > 12*time.Hour)
			require.NotNil(t, f.plan().LastNotificationSent)
			require.True(t, cooldownStart.Sub(*f.plan().LastNotificationSent) < time.Hour)
		})
	}
}
