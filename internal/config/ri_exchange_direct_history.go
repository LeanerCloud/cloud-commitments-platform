package config

import (
	"context"
	"encoding/json"
	"time"
)

// RIExchangeDirectHistoryRow is one direct (interactive) RI exchange as the
// History view shows it, of any provider, status and resolved state. Error is
// the raw stored provider text: callers pass it through
// purchase.SanitizeStoredError and never return it as is.
type RIExchangeDirectHistoryRow struct {
	ID                 string
	Provider           string
	Status             string
	CloudAccountID     *string
	ProviderAccountID  *string
	Region             string
	SourceIDs          []string
	Targets            json.RawMessage
	MaxPaymentDue      string
	MaxPaymentCurrency string
	NetPayable         *string
	NetPayableCurrency string
	Error              string
	ExecutedBy         string
	CreatedAt          time.Time
	CompletedAt        *time.Time
	ResolvedStatus     string
	ResolvedAt         *time.Time
}

// RIExchangeDirectHistoryFilter bounds the History listing. AllAccounts lifts
// the account restriction (and admits rows with no registered account);
// otherwise only rows whose cloud_account_id is in AccountIDs match.
type RIExchangeDirectHistoryFilter struct {
	Since       time.Time
	Limit       int
	AllAccounts bool
	AccountIDs  []string
}

// RIExchangeDirectHistoryStore is the read-only History listing of direct
// exchanges, newest first.
type RIExchangeDirectHistoryStore interface {
	ListRIExchangeDirectHistory(ctx context.Context, f RIExchangeDirectHistoryFilter) ([]RIExchangeDirectHistoryRow, error)
}
