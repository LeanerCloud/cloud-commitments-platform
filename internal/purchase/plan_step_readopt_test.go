package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readoptPlan(mutate func(*config.PurchasePlan)) config.PurchasePlan {
	next := time.Now().Add(-48 * time.Hour)
	p := config.PurchasePlan{ID: "plan-r", Enabled: true, NotificationDaysBefore: 3, NextExecutionDate: &next}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

func TestShouldReadoptExpiredPlan_Admission(t *testing.T) {
	now := time.Now()
	fourEyes := &config.GlobalConfig{RequireDifferentApprover: true, AutoPurchaseEnabled: true}
	recent := now.Add(-time.Hour)
	for _, tc := range []struct {
		name   string
		plan   config.PurchasePlan
		global *config.GlobalConfig
		auto   bool
		listed bool
		want   bool
	}{
		{"manual plan, 4-eyes off", readoptPlan(nil), &config.GlobalConfig{}, true, true, true},
		{"auto plan, 4-eyes on", readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), fourEyes, true, true, true},
		{"auto plan, 4-eyes off is not adopted", readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), &config.GlobalConfig{AutoPurchaseEnabled: true}, true, true, false},
		{"auto plan, kill switch off", readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), fourEyes, false, true, false},
		{"manual plan ignores the kill switch", readoptPlan(nil), &config.GlobalConfig{}, false, true, true},
		{"not listed", readoptPlan(nil), fourEyes, true, false, false},
		{"nil global fails closed", readoptPlan(nil), nil, true, true, false},
		{"disabled plan", readoptPlan(func(p *config.PurchasePlan) { p.Enabled = false }), fourEyes, true, true, false},
		{"dates managed plan", readoptPlan(func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true }), fourEyes, true, true, false},
		{"no next date", readoptPlan(func(p *config.PurchasePlan) { p.NextExecutionDate = nil }), fourEyes, true, true, false},
		{"no veto window", readoptPlan(func(p *config.PurchasePlan) { p.NotificationDaysBefore = 0 }), fourEyes, true, true, false},
		{"notified within the throttle", readoptPlan(func(p *config.PurchasePlan) { p.LastNotificationSent = &recent }), fourEyes, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldReadoptExpiredPlan(tc.plan, tc.global, tc.auto, tc.listed, now))
		})
	}
}

func TestReadoptableExpiredRoot(t *testing.T) {
	expired := func(mutate func(*config.PurchaseExecution)) config.PurchaseExecution {
		return stepRow(config.StatusExpired, func(e *config.PurchaseExecution) {
			e.Recommendations = nil
			if mutate != nil {
				mutate(e)
			}
		})
	}
	acct, id, now := "acct-1", "other", time.Now()
	for _, tc := range []struct {
		name string
		rows []config.PurchaseExecution
		want bool
	}{
		{"single expired root", []config.PurchaseExecution{expired(nil)}, true},
		{"no rows", nil, false},
		{"failed child beside it", []config.PurchaseExecution{expired(nil), stepRow("failed", nil)}, false},
		{"canceled root beside it", []config.PurchaseExecution{expired(nil), stepRow(config.StatusCanceled, nil)}, false},
		{"second expired row", []config.PurchaseExecution{expired(nil), expired(func(e *config.PurchaseExecution) { e.ExecutionID = "row-2" })}, false},
		{"not expired", []config.PurchaseExecution{stepRow("failed", nil)}, false},
		{"child with an account", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.CloudAccountID = &acct })}, false},
		{"retry row", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.RetryAttemptN = 1 })}, false},
		{"retry linked", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.RetryExecutionID = &id })}, false},
		{"approved", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.ApprovedBy = &id })}, false},
		{"executed", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.ExecutedAt = &now })}, false},
		{"completed", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.CompletedAt = &now })}, false},
		{"deferred execution", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.ScheduledExecutionAt = &now })}, false},
		{"skipped approval", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) { e.PreApprovalSkipReason = &id })}, false},
		{"purchased rec", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) {
			e.Recommendations = []config.RecommendationRecord{{ID: "r", Purchased: true}}
		})}, false},
		{"rec with a purchase id", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) {
			e.Recommendations = []config.RecommendationRecord{{ID: "r", PurchaseID: "p-1"}}
		})}, false},
		{"unpurchased old recs are fine", []config.PurchaseExecution{expired(func(e *config.PurchaseExecution) {
			e.Recommendations = []config.RecommendationRecord{{ID: "r"}}
		})}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readoptableExpiredRoot(tc.rows)
			assert.Equal(t, tc.want, got != nil)
		})
	}
}

// readoptStore is the mock store plus the optional re-adoption capability.
type readoptStore struct {
	*MockConfigStore
	listed  map[string]struct{}
	listErr error
	cas     func(id string, date time.Time, requireDifferentApprover bool) (*config.PurchaseExecution, error)
	casCall int
}

func (s *readoptStore) ListPlansWithReadoptableExpiredRoot(_ context.Context, _ time.Time, _ bool) (map[string]struct{}, error) {
	return s.listed, s.listErr
}

func (s *readoptStore) ReadoptExpiredRootTx(_ context.Context, _ pgx.Tx, id string, date, _ time.Time, requireDifferentApprover bool) (*config.PurchaseExecution, error) {
	s.casCall++
	return s.cas(id, date, requireDifferentApprover)
}

func TestReadoptCandidates(t *testing.T) {
	ctx := context.Background()
	global := &config.GlobalConfig{}
	t.Run("store without the capability", func(t *testing.T) {
		m := &Manager{config: new(MockConfigStore)}
		assert.Empty(t, m.readoptCandidates(ctx, global, time.Now()))
	})
	t.Run("list error is skipped", func(t *testing.T) {
		m := &Manager{config: &readoptStore{MockConfigStore: new(MockConfigStore), listErr: errors.New("boom")}}
		assert.Empty(t, m.readoptCandidates(ctx, global, time.Now()))
	})
	t.Run("nil global", func(t *testing.T) {
		m := &Manager{config: &readoptStore{MockConfigStore: new(MockConfigStore), listed: map[string]struct{}{"p": {}}}}
		assert.Empty(t, m.readoptCandidates(ctx, nil, time.Now()))
	})
	t.Run("returns the listed plans", func(t *testing.T) {
		m := &Manager{config: &readoptStore{MockConfigStore: new(MockConfigStore), listed: map[string]struct{}{"p": {}}}}
		require.Contains(t, m.readoptCandidates(ctx, global, time.Now()), "p")
	})
}
