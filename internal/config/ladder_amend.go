package config

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"time"
)

var (
	ErrLadderAmendNotFound = errors.New("ladder tranche not found")
	ErrLadderAmendConflict = errors.New("ladder tranche changed or is no longer editable")
	ErrLadderAmendInvalid  = errors.New("ladder amendment exceeds the original run total or current per-run cap")
	ladderAmountPattern    = regexp.MustCompile(`^(0|[1-9][0-9]{0,13})(\.[0-9]{1,6})?$`)
)

type LadderAmendment struct {
	ExpectedRevision *int64    `json:"expected_revision"`
	ScheduledDate    time.Time `json:"scheduled_date"`
	AmountUSDHr      string    `json:"amount_usd_hr"`
}

func (a LadderAmendment) Validate() error {
	amount, ok := new(big.Rat).SetString(a.AmountUSDHr)
	if !ladderAmountPattern.MatchString(a.AmountUSDHr) || !ok || amount.Sign() <= 0 {
		return errors.New("amount_usd_hr must be a positive decimal with at most six fractional digits")
	}
	if a.ExpectedRevision == nil || *a.ExpectedRevision < 0 || a.ScheduledDate.IsZero() {
		return errors.New("expected_revision must be nonnegative and scheduled_date is required")
	}
	return nil
}

type LadderAmendmentResult struct {
	ID            string    `json:"id"`
	Revision      int64     `json:"revision"`
	ScheduledDate time.Time `json:"scheduled_date"`
	AmountUSDHr   string    `json:"amount_usd_hr"`
	RunTotalUSDHr string    `json:"run_total_usd_hr"`
}

type LadderAmendmentStore interface {
	LadderTrancheScope(context.Context, string) (string, string, error)
	AmendLadderTranche(context.Context, string, string, string, string, LadderAmendment) (*LadderAmendmentResult, error)
}
