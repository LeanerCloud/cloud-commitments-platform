package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// riExchangeDirectHistoryItem is the read-only History view of one direct
// (interactive) exchange. The raw stored error, the executing user id and the
// API key id never leave the server.
type riExchangeDirectHistoryItem struct {
	ID                 string          `json:"id"`
	Provider           string          `json:"provider"`
	Status             string          `json:"status"`
	Region             string          `json:"region"`
	CloudAccountID     *string         `json:"cloud_account_id"`
	ProviderAccountID  *string         `json:"provider_account_id"`
	SourceIDs          []string        `json:"source_ids"`
	Targets            json.RawMessage `json:"targets"`
	MaxPaymentDue      string          `json:"max_payment_due"`
	MaxPaymentCurrency string          `json:"max_payment_currency"`
	NetPayable         *string         `json:"net_payable"`
	NetPayableCurrency string          `json:"net_payable_currency"`
	ErrorSummary       string          `json:"error_summary"`
	ExecutedBy         string          `json:"executed_by"`
	CreatedAt          time.Time       `json:"created_at"`
	CompletedAt        *time.Time      `json:"completed_at"`
	ResolvedStatus     string          `json:"resolved_status"`
	ResolvedAt         *time.Time      `json:"resolved_at"`
}

func newRIExchangeDirectHistoryItem(r *config.RIExchangeDirectHistoryRow) riExchangeDirectHistoryItem {
	return riExchangeDirectHistoryItem{
		ID: r.ID, Provider: r.Provider, Status: r.Status, Region: r.Region,
		CloudAccountID: r.CloudAccountID, ProviderAccountID: r.ProviderAccountID,
		SourceIDs: r.SourceIDs, Targets: r.Targets,
		MaxPaymentDue: r.MaxPaymentDue, MaxPaymentCurrency: r.MaxPaymentCurrency,
		NetPayable: r.NetPayable, NetPayableCurrency: r.NetPayableCurrency,
		ErrorSummary: purchase.SanitizeStoredError(r.Error), ExecutedBy: r.ExecutedBy,
		CreatedAt: r.CreatedAt.UTC(), CompletedAt: r.CompletedAt,
		ResolvedStatus: r.ResolvedStatus, ResolvedAt: r.ResolvedAt,
	}
}

// loadRIExchangeDirectHistory returns the past year of direct exchanges the
// session's account scope covers, newest first. A row with no registered
// account needs an unrestricted scope. Before migration 163 is applied the
// table lacks its resolution columns and the list is empty (N-1 rollout).
func (h *Handler) loadRIExchangeDirectHistory(ctx context.Context, session *Session) ([]riExchangeDirectHistoryItem, error) {
	items := []riExchangeDirectHistoryItem{}
	store, ok := h.config.(config.RIExchangeDirectHistoryStore)
	if !ok {
		return items, nil
	}
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	filter, err := h.riExchangeDirectHistoryFilter(ctx, scope)
	if err != nil {
		return nil, err
	}
	rows, err := store.ListRIExchangeDirectHistory(ctx, filter)
	if errors.Is(err, config.ErrRIExchangeDirectResolveNotReady) {
		return items, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load direct exchange history: %w", err)
	}
	for i := range rows {
		items = append(items, newRIExchangeDirectHistoryItem(&rows[i]))
	}
	return items, nil
}

// riExchangeDirectHistoryFilter turns the account scope into the store filter:
// unrestricted, or the ids of the registered accounts (of any provider) the
// scope covers.
func (h *Handler) riExchangeDirectHistoryFilter(ctx context.Context, scope auth.AccountScope) (config.RIExchangeDirectHistoryFilter, error) {
	filter := config.RIExchangeDirectHistoryFilter{
		Since: time.Now().AddDate(-1, 0, 0), Limit: riExchangeHistoryLimit, AllAccounts: scope.AllowsAll(),
	}
	if filter.AllAccounts {
		return filter, nil
	}
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return filter, fmt.Errorf("failed to list cloud accounts for direct exchange history scope: %w", err)
	}
	byID := make(map[string]config.CloudAccount, len(accounts))
	for i := range accounts {
		byID[accounts[i].ID] = accounts[i]
	}
	for i := range accounts {
		id := accounts[i].ID
		if _, in := riExchangeDirectPairAccount(scope, byID, config.RIExchangeDirectAccountRegion{CloudAccountID: &id}); in {
			filter.AccountIDs = append(filter.AccountIDs, id)
		}
	}
	return filter, nil
}
