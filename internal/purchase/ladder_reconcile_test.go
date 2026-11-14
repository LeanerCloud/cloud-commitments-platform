package purchase

import (
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

func chainNode(id, status string, proof bool) config.LadderChainNode {
	return config.LadderChainNode{ExecutionID: id, Status: status, NoSubmission: proof}
}

var testNow = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func scheduledNode(due time.Time) config.LadderChainNode {
	n := chainNode("e1", "scheduled", false)
	n.ScheduledExecutionAt = &due
	return n
}

func visibleAlways(string) (bool, error) { return true, nil }

// Pure mapping (no DB). The real-database behavior is in
// ladder_reconcile_integration_test.go.
func TestDecideFiredTranche(t *testing.T) {
	linked := chainNode("e1", "failed", true)
	next := "e2"
	linked.RetryExecutionID = &next
	cases := []struct {
		name    string
		chain   []config.LadderChainNode
		visible func(string) (bool, error)
		want    ladder.TrancheStatus
		reason  string
		waiting bool
	}{
		{name: "failed with proof", chain: []config.LadderChainNode{chainNode("e1", "failed", true)}, visible: visibleAlways, want: ladder.TrancheStatusFailed, reason: "no_submission", waiting: false},
		{name: "failed without proof (reaped or outcome unknown)", chain: []config.LadderChainNode{chainNode("e1", "failed", false)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "first attempt reaped, later attempts proven", chain: []config.LadderChainNode{chainNode("e1", "failed", false), chainNode("e2", "failed", true), chainNode("e3", "failed", true)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "every attempt proven", chain: []config.LadderChainNode{chainNode("e1", "failed", true), chainNode("e2", "failed", true)}, visible: visibleAlways, want: ladder.TrancheStatusFailed, reason: "no_submission", waiting: false},
		{name: "canceled tail", chain: []config.LadderChainNode{chainNode("e1", "canceled", false)}, visible: visibleAlways, want: ladder.TrancheStatusFailed, reason: "approval_canceled", waiting: false},
		{name: "expired tail after unproven attempt", chain: []config.LadderChainNode{chainNode("e1", "failed", false), chainNode("e2", "expired", false)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "proof flag ignored on a non-failed row", chain: []config.LadderChainNode{chainNode("e1", "running", true)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "partially completed", chain: []config.LadderChainNode{chainNode("e1", "partially_completed", false)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "completed and visible", chain: []config.LadderChainNode{chainNode("e1", "completed", false)}, visible: visibleAlways, want: ladder.TrancheStatusCompleted, reason: "commitment_visible", waiting: false},
		{name: "completed not visible", chain: []config.LadderChainNode{chainNode("e1", "completed", false)}, visible: func(string) (bool, error) { return false, nil }, want: "", reason: "", waiting: false},
		{name: "completed visibility error", chain: []config.LadderChainNode{chainNode("e1", "completed", false)}, visible: func(string) (bool, error) { return false, errors.New("boom") }, want: "", reason: "", waiting: false},
		{name: "scheduled tail with no due time does not wait", chain: []config.LadderChainNode{chainNode("e1", "scheduled", false)}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "scheduled tail due in the future waits", chain: []config.LadderChainNode{scheduledNode(testNow.Add(time.Hour))}, visible: visibleAlways, want: "", reason: "", waiting: true},
		{name: "scheduled tail overdue within grace waits", chain: []config.LadderChainNode{scheduledNode(testNow.Add(-LadderFiredStaleAfter + time.Minute))}, visible: visibleAlways, want: "", reason: "", waiting: true},
		{name: "scheduled tail overdue past grace does not wait", chain: []config.LadderChainNode{scheduledNode(testNow.Add(-LadderFiredStaleAfter - time.Minute))}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "scheduled tail exactly at the end of its grace does not wait", chain: []config.LadderChainNode{scheduledNode(testNow.Add(-LadderFiredStaleAfter))}, visible: visibleAlways, want: "", reason: "", waiting: false},
		{name: "chain cut short (tail still linked)", chain: []config.LadderChainNode{linked}, visible: visibleAlways, want: "", reason: "", waiting: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := decideFiredTranche(c.chain, testNow, c.visible)
			assert.Equal(t, c.want, v.to)
			assert.Equal(t, c.reason, v.reason)
			assert.Equal(t, c.waiting, v.waiting)
			if c.want == "" {
				assert.NotEmpty(t, v.note, "a tranche left fired must say why")
			}
		})
	}
}
