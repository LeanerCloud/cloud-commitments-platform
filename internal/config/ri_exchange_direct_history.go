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
	CreatedAt          time.Time
	NetPayable         *string
	ResolvedAt         *time.Time
	CompletedAt        *time.Time
	CloudAccountID     *string
	ProviderAccountID  *string
	MaxPaymentDue      string
	ID                 string
	MaxPaymentCurrency string
	NetPayableCurrency string
	Error              string
	ExecutedBy         string
	Region             string
	Status             string
	ResolvedStatus     string
	Provider           string
	Targets            json.RawMessage
	SourceIDs          []string
}

// RIExchangeDirectHistoryFilter bounds the History listing. AllAccounts lifts
// the account restriction (and admits rows with no registered account);
// otherwise only rows whose cloud_account_id is in AccountIDs match.
type RIExchangeDirectHistoryFilter struct {
	Since       time.Time
	AccountIDs  []string
	Limit       int
	AllAccounts bool
}

// RIExchangeDirectHistoryStore is the read-only History listing of direct
// exchanges, newest first.
type RIExchangeDirectHistoryStore interface {
	ListRIExchangeDirectHistory(ctx context.Context, f RIExchangeDirectHistoryFilter) ([]RIExchangeDirectHistoryRow, error)
}
