package config

import (
	"context"
	"time"
)

// RIExchangeDirectResolveSubmittedAfter is how long a submitted AWS direct row
// must have existed before an operator may resolve it: the direct execute
// bounds its claim and provider call by one deadline taken before the row is
// saved (#1841), so no live process can still finalize it after this long.
// Measured on the database clock, like created_at.
const RIExchangeDirectResolveSubmittedAfter = 10 * time.Minute

// RIExchangeDirectResolveNoteMax bounds resolution_note, in bytes.
const RIExchangeDirectResolveNoteMax = 1000

// RIExchangeDirectResolveUnknownCheckAfter is how long after an unknown row's
// completed_at the operator's AWS check must have been made, to allow for
// DescribeReservedInstances eventual consistency.
const RIExchangeDirectResolveUnknownCheckAfter = time.Minute

// RIExchangeDirectResolve asks to settle a stuck AWS direct row on an
// operator's word. Outcome is completed or failed; ExpectedStatus is the
// status (submitted or unknown) the operator saw. Note is the complete text
// stored in resolution_note, built by the caller so a repeat compares equal
// as a whole. CheckedAt is when the operator checked AWS.
type RIExchangeDirectResolve struct {
	ID               string
	Outcome          string
	ExpectedStatus   string
	Note             string
	CheckedAt        time.Time
	ResolvedByUserID *string
	ResolvedBy       string
}

// RIExchangeDirectResolveResult is the row after the call. Status is the
// original status, unchanged. Replayed is true when an identical earlier
// request had already resolved the row and nothing was written.
type RIExchangeDirectResolveResult struct {
	ID             string
	Status         string
	ResolvedStatus string
	ResolvedAt     time.Time
	Note           string
	Replayed       bool
}

// RIExchangeDirectResolveStore is the store surface of the direct-row list and
// resolve endpoints.
type RIExchangeDirectResolveStore interface {
	RIExchangeDirectListStore
	ResolveRIExchangeDirectExecution(ctx context.Context, req RIExchangeDirectResolve) (*RIExchangeDirectResolveResult, error)
}

var _ RIExchangeDirectResolveStore = (*PostgresStore)(nil)
