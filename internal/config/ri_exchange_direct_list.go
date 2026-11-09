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
	ID                 string
	Status             string
	CloudAccountID     *string
	Region             string
	SourceIDs          []string
	Targets            json.RawMessage
	MaxPaymentDue      string
	MaxPaymentCurrency string
	Error              string
	ExecutedBy         string
	CreatedAt          time.Time
	CompletedAt        *time.Time
	ResolvedAt         *time.Time
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
	Allowed  []RIExchangeDirectAccountRegion
	Statuses []string // submitted and/or unknown; empty means both
	RIID     string   // matched like the in-flight guard: trimmed, lower case
	Region   string
	Limit    int
	After    *RIExchangeDirectCursor
}

// RIExchangeDirectListPage is one page, oldest first. Next is nil at the end.
type RIExchangeDirectListPage struct {
	Rows []RIExchangeDirectRow
	Next *RIExchangeDirectCursor
}
