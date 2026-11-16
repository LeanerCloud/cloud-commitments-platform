package config

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// LadderTimelineCursor is an optional (created_at, id) lower bound on the rows
// a timeline read returns. Reads never issue one: each returns the whole scope
// from one snapshot (#668), and the bound is only honored for a client that
// still follows a next_cursor from the earlier paginated API.
type LadderTimelineCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

type LadderTimelineEvent struct {
	ScheduledDate  time.Time            `json:"scheduled_date"`
	CreatedAt      time.Time            `json:"created_at"`
	AmountBasis    *LadderAmountBasis   `json:"amount_basis,omitempty"`
	ExecutionID    *string              `json:"execution_id,omitempty"`
	CommitmentRate *string              `json:"commitment_rate,omitempty"`
	RunID          string               `json:"run_id"`
	AmountUSDHr    string               `json:"amount_usd_hr"`
	ConfigID       string               `json:"config_id"`
	ID             string               `json:"id"`
	ODUSDHr        string               `json:"od_usd_hr"`
	Layer          ladder.LayerType     `json:"layer_type"`
	Term           ladder.Term          `json:"term"`
	Payment        ladder.PaymentOption `json:"payment_option"`
	Status         ladder.TrancheStatus `json:"status"`
	RunStatus      ladder.RunStatus     `json:"run_status"`
	Revision       int64                `json:"revision"`
}

type LadderTimelinePage struct {
	TotalUSDHr   string                `json:"total_usd_hr"`
	TotalODUSDHr string                `json:"total_od_usd_hr"`
	Events       []LadderTimelineEvent `json:"events"`
	TotalCount   int64                 `json:"total_count"`
}

type LadderTimelineRun struct {
	StartedAt         time.Time `json:"started_at"`
	CreatedAt         time.Time `json:"created_at"`
	BaselineUSDHr     *string   `json:"baseline_usd_hr"`
	TargetUSDHr       *string   `json:"target_usd_hr"`
	ExistingUSDHr     *string   `json:"existing_usd_hr"`
	GapUSDHr          *string   `json:"gap_usd_hr"`
	ID                string    `json:"id"`
	ConfigID          string    `json:"config_id"`
	TotalHourlyCommit string    `json:"total_hourly_commit"`
	// ReasonCode is the plan's reason_code, set only when the run could not
	// size its gap (for example "missing_discount_rate"). An ordinary run,
	// including a target-met Hold, has none.
	ReasonCode string                 `json:"reason_code,omitempty"`
	Status     ladder.RunStatus       `json:"status"`
	Actions    []LadderTimelineAction `json:"actions"`
	// TrancheCounts is the run's tranche rows per settled status; the legacy
	// pre-go#554 spelling is folded into Canceled.
	TrancheCounts LadderTrancheCounts `json:"tranche_counts"`
	// DelayedFirstStep marks a run whose earliest tranche is scheduled later
	// than LadderFireGrace after the run started: the claim filter refuses it,
	// so the ramp never buys (plan #70 D7(a)). False for a run without tranches.
	DelayedFirstStep bool `json:"delayed_first_step"`
}

type LadderTrancheCounts struct {
	Scheduled int64 `json:"scheduled"`
	Fired     int64 `json:"fired"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Canceled  int64 `json:"canceled"`
}

type LadderTimelineAction struct {
	Action      string   `json:"action"`
	Layer       string   `json:"layer"`
	Rationale   string   `json:"rationale"`
	DataSources []string `json:"data_sources,omitempty"`
}

type LadderTimelineRunPage struct {
	Runs       []LadderTimelineRun `json:"runs"`
	TotalCount int64               `json:"total_count"`
}

type LadderTimelineStore interface {
	ListLadderTimeline(context.Context, string, string, *LadderTimelineCursor) (*LadderTimelinePage, error)
	ListLadderTimelineRuns(context.Context, string, string, *LadderTimelineCursor) (*LadderTimelineRunPage, error)
}
