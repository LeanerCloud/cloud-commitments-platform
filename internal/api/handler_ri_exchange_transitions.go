package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// riExchangeTransitionError maps a lost pending -> X transition: the record
// expired (410) or another writer moved its status first (409), the same
// codes as an expired or already-handled purchase email link (issue #587).
// The expiry pre-check in checkExchangeToken cannot close the window alone:
// the record can expire, or a concurrent approve or reject can win, between
// that check and the transition. Anything else stays a server error.
//
// handleRequestError does not log ClientErrors, so the 409 and 410 branches
// log the store's error themselves: it names the exchange and its current
// status, which the response body does not.
func riExchangeTransitionError(err error, conflictMsg string) error {
	switch {
	case errors.Is(err, config.ErrRIExchangeExpired):
		logging.Infof("ri-exchange transition lost to expiry: %v", err)
		return NewClientError(410, riExchangeExpiredMsg)
	case errors.Is(err, config.ErrRIExchangeNotInExpectedStatus):
		logging.Infof("ri-exchange transition lost to a concurrent writer: %v", err)
		return NewClientError(409, conflictMsg)
	default:
		return fmt.Errorf("failed to transition exchange status: %w", err)
	}
}

// failExchange marks an exchange as failed, logging if the DB write also fails.
//
// Every caller runs after the pending -> processing transition committed, so
// the write is detached from the request (#790): a cancel there would
// otherwise strand the record in "processing", where the approval retry gets
// 409 from the transition guard and the row keeps counting against the daily cap.
//
//nolint:unparam // (result, error) shape lets executeApprovedExchange tail-call it; a failed exchange is a 200 "failed" body, not an error
func (h *Handler) failExchange(ctx context.Context, id, reason string) (any, error) {
	ctx, cancel := ctxutil.DetachedWithTimeout(ctx, riExchangeTerminalSaveTimeout)
	defer cancel()
	if failErr := h.config.FailRIExchange(ctx, id, reason); failErr != nil {
		logging.Errorf("failed to mark exchange %s as failed (DB may be unavailable): %v", id, failErr)
	}
	return map[string]any{"status": "failed", "reason": reason}, nil
}

// reserveApprovedExchange reserves the approval's spend ceiling under the caps
// and returns it as text and as a decimal. A non-empty refusal is the
// caller-safe reason to fail the exchange with: the cap figures, or only the
// shared source RI ids, never the blocking exchange (#1050, #1428).
func (h *Handler) reserveApprovedExchange(ctx context.Context, id string, globalCfg *config.GlobalConfig) (ceiling string, effectiveCap *big.Rat, refusal string) {
	dailyCapUSD, perExchangeCapUSD, reason := exchangeCapStrings(globalCfg)
	if reason != "" {
		return "", nil, reason
	}
	ceiling, err := h.config.ReserveApprovedRIExchange(ctx, id, dailyCapUSD, perExchangeCapUSD)
	if err != nil {
		logging.Warnf("RI exchange %s reservation refused: %v", id, err)
		if errors.Is(err, config.ErrRIExchangeDailyCapExceeded) || errors.Is(err, config.ErrRIExchangeSourceInFlight) {
			return "", nil, err.Error()
		}
		return "", nil, "daily spending cap check failed"
	}
	effectiveCap, ok := new(big.Rat).SetString(ceiling)
	if !ok {
		return "", nil, fmt.Sprintf("reserved ceiling %q is not a decimal", ceiling)
	}
	return ceiling, effectiveCap, ""
}

// leaveExchangeOutcomeUnknown keeps a row whose accept response was lost in
// processing with its reserved ceiling: AWS may have exchanged the RIs, so a
// failed, retryable row would invite a duplicate exchange (#1050). The note is
// fixed text plus the sanitized reason; the raw error stays in the log. A
// failed note write is logged and the outcome is still reported.
func (h *Handler) leaveExchangeOutcomeUnknown(ctx context.Context, id, reason string) map[string]any {
	note := fmt.Sprintf("outcome unknown: %s; verify the source RIs and target reservation at AWS, then POST /api/ri-exchange/resolve/%s", reason, id)
	if err := h.config.NoteRIExchangeOutcomeUnknown(ctx, id, note); err != nil {
		logging.Errorf("failed to note outcome unknown on RI exchange %s: %v", id, err)
	}
	return map[string]any{"status": "outcome_unknown", "message": note}
}

// retryCompleteWithPayment calls CompleteRIExchangeWithPayment up to
// maxLedgerAttempts times, logging retries. Returns a non-nil error if all
// attempts fail. Extracted from executeApprovedExchange to reduce its
// cyclomatic complexity (H4 fix).
func (h *Handler) retryCompleteWithPayment(ctx context.Context, id, exchangeID, acceptedPaymentDue string) error {
	const maxAttempts = 3
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = h.config.CompleteRIExchangeWithPayment(ctx, id, exchangeID, acceptedPaymentDue)
		if err == nil {
			return nil
		}
		if attempt < maxAttempts {
			logging.Warnf("ledger write retry %d/%d for exchange %s after money moved: %v",
				attempt, maxAttempts, id, err)
		}
	}
	return err
}

// handlerAcceptedAmount extracts the confirmed payment amount from a fresh
// Execute quote, falling back to fallback when freshQ is nil or empty (H3 fix).
//
// The empty case no longer means "zero-cost exchange": since #1964 an absent
// PaymentDue is refused by checkInitialQuote and checkReQuote before Accept,
// and a genuine zero arrives as an explicit "0.000000". Execute therefore
// cannot return successfully with an empty amount, so this mirrors
// exchange.acceptedAmountFromQuote and returns the caller's fallback rather
// than fabricating "0".
func handlerAcceptedAmount(freshQ *exchange.ExchangeQuoteSummary, fallback string) string {
	if freshQ != nil && freshQ.PaymentDueUSDStr != "" {
		return freshQ.PaymentDueUSDStr
	}
	return fallback
}

// exchangeCapStrings validates the spending-cap configuration and returns the
// daily and per-exchange caps as decimal strings for the atomic reservation.
// A non-empty reason means the caller must forward it to failExchange.
func exchangeCapStrings(cfg *config.GlobalConfig) (daily, perExchange, reason string) {
	if cfg.RIExchangeMaxDailyUSD == 0 {
		return "", "", "daily spending cap is not configured (RIExchangeMaxDailyUSD is 0)"
	}
	if cfg.RIExchangeMaxPerExchangeUSD == 0 {
		return "", "", "per-exchange spending cap is not configured (RIExchangeMaxPerExchangeUSD is 0)"
	}
	daily = new(big.Rat).SetFloat64(cfg.RIExchangeMaxDailyUSD).FloatString(6)
	perExchange = new(big.Rat).SetFloat64(cfg.RIExchangeMaxPerExchangeUSD).FloatString(6)
	return daily, perExchange, ""
}
