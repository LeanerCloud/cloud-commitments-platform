package config

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ==========================================
// COMMITMENT LADDERING
// ==========================================

// Ladder default constants. These alias the canonical values in pkg/ladder so
// that internal/config callers can reference them by name without importing
// pkg/ladder directly. The string-typed mode/cadence values are also sourced
// from pkg/ladder to guarantee the DB and the engine always use the same tokens.

// DefaultLadderTargetCoverage is the default commitment coverage target (%).
// Aliases ladder.DefaultTargetCoveragePct so only one source of truth exists.
const DefaultLadderTargetCoverage = ladder.DefaultTargetCoveragePct

// DefaultLadderBufferFraction is the default fraction of the base allocation
// reserved in short-term / convertible buffer commitments.
const DefaultLadderBufferFraction = ladder.DefaultBufferFraction

// DefaultLadderBaselinePercentile is the default usage percentile used to
// anchor the base commitment layer.
const DefaultLadderBaselinePercentile = ladder.DefaultBaselinePercentile

// DefaultLadderLookbackDays is the default historical window (days) used to
// compute the usage baseline.
const DefaultLadderLookbackDays = ladder.DefaultLookbackDays

// DefaultLadderBufferUtilThreshold is the default buffer-layer utilization %
// below which the engine emits a reshape recommendation.
const DefaultLadderBufferUtilThreshold = ladder.DefaultBufferUtilizationThresholdPct

// DefaultLadderMaxActionsPerRun is the default cap on the number of
// PlannedActions the engine may execute per run.
const DefaultLadderMaxActionsPerRun = 10

// MaxLadderActionsPerRun is the ceiling enforced at validation time. A value
// above this is almost certainly a misconfiguration and should fail loud rather
// than fan out unbounded actions.
const MaxLadderActionsPerRun = 50

// LadderConfigDB is the DB-persistence mirror of pkg/ladder.LadderConfig.
// It stores one per-account, per-provider ladder configuration row and is
// used by the store layer (GetLadderConfig / UpsertLadderConfig) and the API
// handler. Mode and Cadence are plain strings whose valid values are defined
// by pkg/ladder (ModeEmailApproval, ModeAutoApprove, CadenceDaily,
// CadenceWeekly). Validation calls pkg/ladder's Parse* functions so the
// internal/config package never redefines those constants.
//
// MaxHourlyCommitPerRun is a pointer because nil means "no cap" (distinct from
// 0, which would cap all spending). All numeric money fields follow the project
// rule: absent = nil/pointer, never 0.
// Field order is optimized for govet fieldalignment (pointer-containing fields
// grouped first to shrink the GC pointer-scan range, then scalars, bool last).
// It intentionally does not follow the logical/SQL column order; see the
// scanLadderConfig / Upsert SQL for the wire order.
type LadderConfigDB struct {
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
	// MaxHourlyCommitPerRun caps the total hourly commitment delta a single run
	// may purchase. nil means no cap.
	MaxHourlyCommitPerRun *float64 `json:"max_hourly_commit_per_run,omitempty"`
	// MaxHourlyCommitPerSweep is the rolling-24h ceiling over all sweeps. It is
	// not part of the ladder_configs upsert: the API reads and writes it through
	// GetLadderSweepCeiling/SetLadderSweepCeiling so an unmigrated schema keeps
	// the four config queries working. nil means not set (the sweep refuses).
	MaxHourlyCommitPerSweep    *float64        `json:"max_hourly_commit_per_sweep,omitempty"`
	CloudAccountID             string          `json:"cloud_account_id"`
	Provider                   string          `json:"provider"`
	Mode                       string          `json:"mode"`    // ladder.ModeEmailApproval | ladder.ModeAutoApprove
	Cadence                    string          `json:"cadence"` // ladder.CadenceDaily | ladder.CadenceWeekly
	ID                         string          `json:"id"`
	RampSchedule               json.RawMessage `json:"ramp_schedule"`
	BufferUtilizationThreshold float64         `json:"buffer_utilization_threshold"`
	LookbackDays               int             `json:"lookback_days"`
	MaxActionsPerRun           int             `json:"max_actions_per_run"`
	// BaselinePercentile is the statistical percentile used to anchor the base
	// commitment layer. Must be in (0, 50].
	BaselinePercentile float64 `json:"baseline_percentile"`
	BufferFraction     float64 `json:"buffer_fraction"`
	TargetCoverage     float64 `json:"target_coverage"`
	Enabled            bool    `json:"enabled"`
}

// LadderRunDB mirrors the ladder_runs table (migration 000080).
// Monetary snapshot columns are *float64 (nullable, NEVER 0-coerced:
// NULL means "not computed", not "$0"). Field order minimizes GC
// pointer-scan range: explicit pointer fields come before scalars.
type LadderRunDB struct {
	// Required timestamps.
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
	// Nullable FK and optional text fields (all pointer types).
	FireAt                 *time.Time `json:"fire_at,omitempty"`
	Cadence                *string    `json:"cadence,omitempty"`
	CompletedAt            *time.Time `json:"completed_at,omitempty"`
	ApprovalTokenHash      *string    `json:"approval_token_hash,omitempty"`
	ApprovalTokenExpiresAt *time.Time `json:"approval_token_expires_at,omitempty"`
	ApprovedBy             *string    `json:"approved_by,omitempty"`
	CancelledBy            *string    `json:"cancelled_by,omitempty"`
	// Nullable monetary snapshot: nil means "not computed", never $0.
	BaselineUSDHr *float64 `json:"baseline_usd_hr,omitempty"`
	// Mode and Cadence are nullable in the DB (populated from LadderConfigDB
	// at run creation time; nil only for legacy / partially-failed rows).
	Mode          *string  `json:"mode,omitempty"`
	ConfigID      *string  `json:"config_id,omitempty"`
	TargetUSDHr   *float64 `json:"target_usd_hr,omitempty"`
	ExistingUSDHr *float64 `json:"existing_usd_hr,omitempty"`
	GapUSDHr      *float64 `json:"gap_usd_hr,omitempty"`
	// Required string / enum fields.
	ID     string           `json:"id"`
	Status ladder.RunStatus `json:"status"`
	// Plan JSON blob (JSONB, NOT NULL DEFAULT '{}').
	Plan json.RawMessage `json:"plan"`
	// Non-nullable accumulator totals (initialised to 0, not measurements;
	// zero is a meaningful value for these counters unlike the monetary snapshot).
	EstimatedSavings  float64 `json:"estimated_savings"`
	TotalUpfrontCost  float64 `json:"total_upfront_cost"`
	TotalHourlyCommit float64 `json:"total_hourly_commit"`
}

// LadderTrancheDB mirrors the ladder_tranches table (migration 000081).
// One row per ramp step per allocation; persisted as status=scheduled
// audit rows only in PR-2 (no firing sweep wired yet). Field order
// minimizes GC pointer-scan range.
type LadderTrancheDB struct {
	ScheduledDate time.Time `json:"scheduled_date"`
	CreatedAt     time.Time `json:"created_at"`
	RunID         *string   `json:"run_id,omitempty"`
	ExecutionID   *string   `json:"execution_id,omitempty"` // references purchase_executions.execution_id
	// SettledAt is stamped by TransitionLadderTrancheTx when the tranche
	// becomes completed; a CHECK requires it on every completed row.
	SettledAt *time.Time `json:"settled_at,omitempty"`
	// Nil basis preserves unproven history; native rates are lossless rational text.
	AmountBasis    *LadderAmountBasis `json:"amount_basis,omitempty"`
	CommitmentRate *string            `json:"commitment_rate,omitempty"`
	// Family is the EC2 instance family a tranche is allocated to; empty means
	// no family (legacy rows and every layer except EC2 Instance SP).
	Family string `json:"family,omitempty"`
	// Nullable FK pointers.
	ConfigID *string `json:"config_id,omitempty"`
	// Required fields.
	ID            string               `json:"id"`
	PaymentOption ladder.PaymentOption `json:"payment_option"`
	Status        ladder.TrancheStatus `json:"status"`
	Term          ladder.Term          `json:"term"`
	LayerType     ladder.LayerType     `json:"layer_type"`
	// Monetary and timing.
	AmountUSDHr float64 `json:"amount_usd_hr"`
}

// IsUSDCommitmentCurrency is the one rule for "this CommitmentCurrency is
// USD": whitespace-trimmed and case-insensitive. Empty (unknown) is not USD.
func IsUSDCommitmentCurrency(currency string) bool {
	return strings.EqualFold(strings.TrimSpace(currency), "USD")
}
