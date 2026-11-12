package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type attachableStore struct {
	*MockConfigStore
	listed map[string]struct{}
	err    error
}

func (s *attachableStore) ListPlansWithAttachableEmptyRoot(context.Context, time.Time) (map[string]struct{}, error) {
	return s.listed, s.err
}

func TestAttachCandidates(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, (&Manager{config: new(MockConfigStore)}).attachCandidates(ctx, time.Now()), "store without the capability")
	assert.Empty(t, (&Manager{config: &attachableStore{MockConfigStore: new(MockConfigStore), err: errors.New("boom")}}).attachCandidates(ctx, time.Now()), "read error is skipped")
	require.Contains(t, (&Manager{config: &attachableStore{MockConfigStore: new(MockConfigStore), listed: map[string]struct{}{"p": {}}}}).attachCandidates(ctx, time.Now()), "p")
}

func TestShouldAttachEmptyRoot(t *testing.T) {
	now := time.Now()
	far := now.Add(30 * 24 * time.Hour)
	recent, old := now.Add(-time.Hour), now.Add(-25*time.Hour)
	plan := func(mutate func(*config.PurchasePlan)) config.PurchasePlan {
		p := config.PurchasePlan{ID: "p", Enabled: true, NotificationDaysBefore: 3, NextExecutionDate: &far}
		if mutate != nil {
			mutate(&p)
		}
		return p
	}
	for _, tc := range []struct {
		name   string
		plan   config.PurchasePlan
		auto   bool
		listed bool
		want   bool
	}{
		{"listed, plan date far away", plan(nil), true, true, true},
		{"not listed", plan(nil), true, false, false},
		{"disabled", plan(func(p *config.PurchasePlan) { p.Enabled = false }), true, true, false},
		{"auto plan, kill switch off", plan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), false, true, false},
		{"manual plan ignores the kill switch", plan(nil), false, true, true},
		{"no next date", plan(func(p *config.PurchasePlan) { p.NextExecutionDate = nil }), true, true, false},
		{"inside the 24h throttle", plan(func(p *config.PurchasePlan) { p.LastNotificationSent = &recent }), true, true, false},
		{"past the 24h throttle", plan(func(p *config.PurchasePlan) { p.LastNotificationSent = &old }), true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldAttachEmptyRoot(tc.plan, tc.auto, tc.listed, now))
		})
	}
}
