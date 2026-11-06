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
		{"failed with proof", []config.LadderChainNode{chainNode("e1", "failed", true)}, visibleAlways, ladder.TrancheStatusFailed, "no_submission", false},
		{"failed without proof (reaped or outcome unknown)", []config.LadderChainNode{chainNode("e1", "failed", false)}, visibleAlways, "", "", false},
		{"first attempt reaped, later attempts proven", []config.LadderChainNode{chainNode("e1", "failed", false), chainNode("e2", "failed", true), chainNode("e3", "failed", true)}, visibleAlways, "", "", false},
		{"every attempt proven", []config.LadderChainNode{chainNode("e1", "failed", true), chainNode("e2", "failed", true)}, visibleAlways, ladder.TrancheStatusFailed, "no_submission", false},
		{"canceled tail", []config.LadderChainNode{chainNode("e1", "canceled", false)}, visibleAlways, ladder.TrancheStatusFailed, "approval_canceled", false},
		{"legacy spelling tail", []config.LadderChainNode{chainNode("e1", config.LegacyStatusCanceled, false)}, visibleAlways, ladder.TrancheStatusFailed, "approval_canceled", false},
		{"expired tail after unproven attempt", []config.LadderChainNode{chainNode("e1", "failed", false), chainNode("e2", "expired", false)}, visibleAlways, "", "", false},
		{"proof flag ignored on a non-failed row", []config.LadderChainNode{chainNode("e1", "running", true)}, visibleAlways, "", "", false},
		{"partially completed", []config.LadderChainNode{chainNode("e1", "partially_completed", false)}, visibleAlways, "", "", false},
		{"completed and visible", []config.LadderChainNode{chainNode("e1", "completed", false)}, visibleAlways, ladder.TrancheStatusCompleted, "commitment_visible", false},
		{"completed not visible", []config.LadderChainNode{chainNode("e1", "completed", false)}, func(string) (bool, error) { return false, nil }, "", "", false},
		{"completed visibility error", []config.LadderChainNode{chainNode("e1", "completed", false)}, func(string) (bool, error) { return false, errors.New("boom") }, "", "", false},
		{"scheduled tail with no due time does not wait", []config.LadderChainNode{chainNode("e1", "scheduled", false)}, visibleAlways, "", "", false},
		{"scheduled tail due in the future waits", []config.LadderChainNode{scheduledNode(testNow.Add(time.Hour))}, visibleAlways, "", "", true},
		{"scheduled tail overdue within grace waits", []config.LadderChainNode{scheduledNode(testNow.Add(-LadderFiredStaleAfter + time.Minute))}, visibleAlways, "", "", true},
		{"scheduled tail overdue past grace does not wait", []config.LadderChainNode{scheduledNode(testNow.Add(-LadderFiredStaleAfter - time.Minute))}, visibleAlways, "", "", false},
		{"chain cut short (tail still linked)", []config.LadderChainNode{linked}, visibleAlways, "", "", false},
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
