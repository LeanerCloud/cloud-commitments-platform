package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// getRIExchangeConfig returns the current RI exchange automation settings.
func (h *Handler) getRIExchangeConfig(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	if _, err := h.requirePermission(ctx, req, "view", "config"); err != nil {
		return nil, err
	}

	globalCfg, err := h.config.GetGlobalConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	return &RIExchangeConfigResponse{
		AutoExchangeEnabled:      globalCfg.RIExchangeEnabled,
		Mode:                     globalCfg.RIExchangeMode,
		UtilizationThreshold:     globalCfg.RIExchangeUtilizationThreshold,
		MaxPaymentPerExchangeUSD: globalCfg.RIExchangeMaxPerExchangeUSD,
		MaxPaymentDailyUSD:       globalCfg.RIExchangeMaxDailyUSD,
		LookbackDays:             globalCfg.RIExchangeLookbackDays,
	}, nil
}

// updateRIExchangeConfig updates the RI exchange automation settings.
func (h *Handler) updateRIExchangeConfig(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "update", "config")
	if err != nil {
		return nil, err
	}

	var body RIExchangeConfigUpdateRequest
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}

	if err := body.validate(); err != nil {
		return nil, err
	}
	if err := h.requireGlobalConfigScope(ctx, session); err != nil {
		return nil, err
	}
	// Resolved before the transaction: the closure below runs while it holds a
	// pooled connection and must not need a second one (issue #1517).
	riExchangeGrant := h.resolveGrant(ctx, req, auth.ResourceRIExchange, riExchangeAutoModeScopeMsg)

	// Route through the serialized read-modify-write so this partial update
	// (only the ri_exchange_* columns) shares the advisory lock with other
	// global_config writers. An unlocked GetGlobalConfig -> mutate ->
	// SaveGlobalConfig here would write all 21 columns and could silently
	// revert a concurrent kill-switch toggle done via UpdateGlobalConfigAtomic.
	// The closure mutates ONLY the ri_exchange_* fields; body.validate above
	// already validated the inputs, so (matching the prior behavior) we do not
	// re-run the whole-config Validate here.
	if _, err := h.config.UpdateGlobalConfigAtomic(ctx, func(existing *config.GlobalConfig) error {
		// Snapshot before mutating: the auto-mode gate compares the stored state
		// against the proposed one, and `existing` becomes the proposed state
		// below. Inside the closure so the comparison reads the same
		// advisory-locked row the write lands on -- checking beforehand would
		// race a concurrent writer between the check and the write.
		before := riExchangeArmStateOf(existing)

		existing.RIExchangeEnabled = body.AutoExchangeEnabled
		existing.RIExchangeMode = body.Mode
		existing.RIExchangeUtilizationThreshold = body.UtilizationThreshold
		existing.RIExchangeMaxPerExchangeUSD = body.MaxPaymentPerExchangeUSD
		existing.RIExchangeMaxDailyUSD = body.MaxPaymentDailyUSD
		existing.RIExchangeLookbackDays = body.LookbackDays

		return requireRIExchangeAutoModeGrant(before, riExchangeArmStateOf(existing), riExchangeGrant)
	}); err != nil {
		// A refused write is not a failed save. Surface the gate's 403 (and any
		// other ClientError the closure raises) unwrapped, so the response says
		// permission denied rather than blaming the store.
		if ce, ok := IsClientError(err); ok {
			return nil, ce
		}
		return nil, fmt.Errorf("failed to save config: %w", err)
	}

	return &StatusResponse{Status: "updated"}, nil
}

// getRIExchangeHistory returns RI exchange records from the last 12 months.
func (h *Handler) getRIExchangeHistory(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	records, err := h.loadRIExchangeHistory(ctx, session)
	if err != nil {
		return nil, err
	}

	// Strip approval tokens — single-use secrets must not be included in
	// a read-only response that could be cached, logged, or screen-shared.
	// Stored errors may hold provider text; only known server notes pass.
	for i := range records {
		records[i].ApprovalToken = ""
		records[i].Error = purchase.SanitizeStoredError(records[i].Error)
	}

	return &RIExchangeHistoryResponse{Records: records}, nil
}

// riExchangeHistoryLimit caps how many exchange records one history request
// returns. For a scoped caller it applies after the account restriction.
const riExchangeHistoryLimit = 500

// loadRIExchangeHistory returns the past year of exchange records the session
// may see, newest first. A scoped caller's account restriction is pushed into
// the store query so the limit counts only that caller's records: filtering
// the newest records of every account afterwards could leave a scoped user
// with a truncated or empty history (issue #308).
//
// RI exchange records carry the 12-digit AWS account id from STS, so it is
// resolved to its single registered AWS account and the scope is matched by
// that account's UUID. An unregistered or ambiguous id matches only a literal
// entry in the allow-list. A lookup failure errors out.
func (h *Handler) loadRIExchangeHistory(ctx context.Context, session *Session) ([]config.RIExchangeRecord, error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	since := time.Now().AddDate(-1, 0, 0)
	if allowed.AllowsAll() {
		records, loadErr := h.config.GetRIExchangeHistory(ctx, since, riExchangeHistoryLimit)
		if loadErr != nil {
			return nil, fmt.Errorf("failed to load exchange history: %w", loadErr)
		}
		return records, nil
	}

	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts for exchange history scope: %w", err)
	}
	ids := exchangeAccountIDsInScope(allowed, accounts)
	records, err := h.config.GetRIExchangeHistoryForAccounts(ctx, since, riExchangeHistoryLimit, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to load exchange history: %w", err)
	}
	// Defense in depth: re-check every returned row against the scope, so a
	// store that ignored the account restriction still cannot leak another
	// account's exchanges.
	filtered := make([]config.RIExchangeRecord, 0, len(records))
	for i := range records {
		if exchangeRecordInScope(allowed, accounts, records[i].AccountID) {
			filtered = append(filtered, records[i])
		}
	}
	return filtered, nil
}
