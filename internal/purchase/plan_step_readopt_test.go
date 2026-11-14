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
		global *config.GlobalConfig
		plan   config.PurchasePlan
		name   string
		auto   bool
		listed bool
		want   bool
	}{
		{name: "manual plan, 4-eyes off", plan: readoptPlan(nil), global: &config.GlobalConfig{}, auto: true, listed: true, want: true},
		{name: "auto plan, 4-eyes on", plan: readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), global: fourEyes, auto: true, listed: true, want: true},
		{name: "auto plan, 4-eyes off is not adopted", plan: readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), global: &config.GlobalConfig{AutoPurchaseEnabled: true}, auto: true, listed: true, want: false},
		{name: "auto plan, kill switch off", plan: readoptPlan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), global: fourEyes, auto: false, listed: true, want: false},
		{name: "manual plan ignores the kill switch", plan: readoptPlan(nil), global: &config.GlobalConfig{}, auto: false, listed: true, want: true},
		{name: "not listed", plan: readoptPlan(nil), global: fourEyes, auto: true, listed: false, want: false},
		{name: "nil global fails closed", plan: readoptPlan(nil), global: nil, auto: true, listed: true, want: false},
		{name: "disabled plan", plan: readoptPlan(func(p *config.PurchasePlan) { p.Enabled = false }), global: fourEyes, auto: true, listed: true, want: false},
		{name: "dates managed plan", plan: readoptPlan(func(p *config.PurchasePlan) { p.RampSchedule.DatesManaged = true }), global: fourEyes, auto: true, listed: true, want: false},
		{name: "no next date", plan: readoptPlan(func(p *config.PurchasePlan) { p.NextExecutionDate = nil }), global: fourEyes, auto: true, listed: true, want: false},
		{name: "no veto window", plan: readoptPlan(func(p *config.PurchasePlan) { p.NotificationDaysBefore = 0 }), global: fourEyes, auto: true, listed: true, want: false},
		{name: "notified within the throttle", plan: readoptPlan(func(p *config.PurchasePlan) { p.LastNotificationSent = &recent }), global: fourEyes, auto: true, listed: true, want: false},
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
