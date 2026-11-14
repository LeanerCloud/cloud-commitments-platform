package config

import (
	"encoding/json"
	"time"
)

// RIExchangeDirectRow is an AWS direct-execute audit row as the operator
// resolve API reads it. It carries no executing user id or API key id. Error
// is the raw stored provider text: callers pass it through
// purchase.SanitizeStoredError and never return it as is.
type RIExchangeDirectRow struct {
	CreatedAt          time.Time
	ResolvedAt         *time.Time
	CloudAccountID     *string
	CompletedAt        *time.Time
	MaxPaymentDue      string
	ID                 string
	MaxPaymentCurrency string
	Error              string
	ExecutedBy         string
	Region             string
	Status             string
	Targets            json.RawMessage
	SourceIDs          []string
}

// RIExchangeDirectAccountRegion is one (account, region) pair of a row. A nil
// CloudAccountID is a row whose deployment mapped to no registered account.
type RIExchangeDirectAccountRegion struct {
	CloudAccountID *string
	Region         string
}

// RIExchangeDirectCursor is the keyset position after the last row of a page.
type RIExchangeDirectCursor struct {
	CreatedAt time.Time
	ID        string
}

// RIExchangeDirectListFilter selects unresolved AWS direct rows. Allowed is
// the set of (account, region) pairs the caller may see, derived by the
// handler from the caller's own authorization; an empty set returns nothing.
type RIExchangeDirectListFilter struct {
	After    *RIExchangeDirectCursor
	RIID     string // matched like the in-flight guard: trimmed, lower case
	Region   string
	Allowed  []RIExchangeDirectAccountRegion
	Statuses []string // submitted and/or unknown; empty means both
	Limit    int
}

// RIExchangeDirectListPage is one page, oldest first. Next is nil at the end.
type RIExchangeDirectListPage struct {
	Next *RIExchangeDirectCursor
	Rows []RIExchangeDirectRow
}
