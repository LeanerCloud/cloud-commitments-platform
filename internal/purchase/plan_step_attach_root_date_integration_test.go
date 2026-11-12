//go:build integration

package purchase

import (
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// TestTickAttachesEmptyRootByItsOwnDate: the root sits a day and a half past
// the end of the notice window, as it does once the daily tick of the day
// before was skipped. The plan's next date is the root's date, so the old test
// (whole days until the plan date <= N) does not admit the plan; the root's own
// date does. A root three days past the window is not attached early.
func TestTickAttachesEmptyRootByItsOwnDate(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		name   string
		offset time.Duration // root date relative to now; N is 3 days
		want   bool
	}{
		{"missed_tick_next_day_attaches", 3*day + 36*time.Hour, true},
		{"not_yet_in_window_not_attached_early", 6 * day, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCumFixture(t, 100)
			f.src.shown = func() []config.RecommendationRecord {
				return []config.RecommendationRecord{cumRec(f.account, "m5.t0", 100)}
			}
			sender := newCountingSender(f.ctx)
			f.m.email = sender
			date := time.Now().Add(tc.offset).Truncate(time.Microsecond)
			plan := f.plan()
			plan.NotificationDaysBefore, plan.NextExecutionDate = 3, &date
			require.NoError(t, f.store.UpdatePurchasePlan(f.ctx, plan))
			root := &config.PurchaseExecution{PlanID: f.planID, ExecutionID: uuid.NewString(), StepNumber: 1,
				Status: "pending", ScheduledDate: date}
			require.NoError(t, f.store.SavePurchaseExecution(f.ctx, root))

			_, err := f.m.SendUpcomingPurchaseNotifications(f.ctx)
			require.NoError(t, err)

			rows := f.stepRows(1)
			require.Len(t, rows, 1)
			require.Equal(t, root.ExecutionID, rows[0].ExecutionID, "the pre-created root is the row that attaches")
			require.True(t, date.Equal(rows[0].ScheduledDate), "the root keeps its own date")
			if tc.want {
				require.NotEmpty(t, rows[0].Recommendations)
			} else {
				require.Empty(t, rows[0].Recommendations)
			}
		})
	}
}
