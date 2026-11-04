package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlocksManagedDelete(t *testing.T) {
	cases := []struct {
		status       string
		noSubmission bool
		settled      bool
		blocks       bool
	}{
		{StatusFailed, false, false, true},
		{StatusFailed, false, true, false},
		{StatusFailed, true, false, false},
		{"pending", false, true, true},
		{"running", false, true, true},
		{StatusCompleted, false, false, false},
		{StatusPartiallyCompleted, false, false, false},
		{StatusCanceled, false, false, false},
		{StatusExpired, false, false, false},
		{"some_future_status", false, true, true},
	}
	for _, tc := range cases {
		e := &PurchaseExecution{Status: tc.status, NoSubmission: tc.noSubmission}
		require.Equal(t, tc.blocks, blocksManagedDelete(e, tc.settled), "%+v", tc)
	}
}

func TestSettlementRecordReplayMatchesOnlyTheSameReason(t *testing.T) {
	req := ExecutionSettleRequest{Reason: `checked "eu" account`}
	record := settlementRecord(req, settleChainTail{id: "tail", status: StatusCompleted})
	require.Equal(t, `operator:settled_unknown_outcome: ack=true chain_tail=tail tail_status=completed reason="checked \"eu\" account"`, record)
	require.True(t, settlementHasReason(record, req.Reason))
	require.False(t, settlementHasReason(record, "account"))
	require.False(t, settlementHasReason(record, `checked "eu" account2`))
}

// The handler and the store count the reason the same way, in characters
// (review on #1241: 300 four-byte characters passed the handler, then the
// store refused them as more than 1000 bytes).
func TestValidateSettleRequestCountsCharacters(t *testing.T) {
	base := ExecutionSettleRequest{ExecutionID: "e", Actor: "a", Acknowledge: true}
	ok := base
	ok.Reason = strings.Repeat("\U0001F600", ExecutionSettleReasonMax)
	require.NoError(t, validateSettleRequest(ok))
	long := base
	long.Reason = strings.Repeat("x", ExecutionSettleReasonMax+1)
	require.ErrorIs(t, validateSettleRequest(long), ErrExecutionSettleInvalid)
}
