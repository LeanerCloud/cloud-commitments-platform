package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	armreservations "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

// calculateAzureRevoke handles GET /api/purchases/revoke/calculate/{id}.
// It runs CalculateRefund against Azure and returns the quoted refund amount
// and currency so the frontend can show the user a confirmation modal before
// the destructive POST /revoke call.
//
// This is the first step of the two-step quote-then-confirm revoke UX
// (issue #290 Finding #4). No state is mutated; the result is used by the
// frontend to populate revokeConfirmBody.
func (h *Handler) calculateAzureRevoke(ctx context.Context, req *events.LambdaFunctionURLRequest, purchaseID string) (any, error) {
	_, orderID, reservationID, count, err := h.validateAzureRevokeRequest(ctx, req, purchaseID)
	if err != nil {
		return nil, err
	}

	calcClient, err := h.buildAzureCalculateRefundClient()
	if err != nil {
		return nil, fmt.Errorf("revoke/calculate: %w", err)
	}

	quantity := int32(count) // #nosec G115 -- Azure reservation count bounded by API limits (<<math.MaxInt32) //nolint:gosec
	calcResp, err := calcClient.Post(ctx, orderID, armreservations.CalculateRefundRequest{
		Properties: &armreservations.CalculateRefundRequestProperties{
			ReservationToReturn: &armreservations.ReservationToReturn{
				ReservationID: &reservationID,
				Quantity:      &quantity,
			},
			Scope: toPtr("Reservation"),
		},
	}, nil)
	if err != nil {
		if isAzureClientError(err) {
			return nil, azureRefundRejected("Azure refund calculation rejected", purchaseID, err)
		}
		return nil, fmt.Errorf("revoke/calculate: CalculateRefund failed: %w", err)
	}

	refundAmount, refundCurrency := extractAzureRefundQuote(calcResp)
	if refundAmount == nil || strings.TrimSpace(refundCurrency) == "" {
		return nil, NewClientError(422, "Azure returned no refund amount or currency for this reservation; cannot quote a refund, contact Azure Support to request one")
	}
	return &revokeQuoteResult{
		RefundAmount:   *refundAmount,
		RefundCurrency: refundCurrency,
		QuotedAt:       time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// validateAzureRevokeRequest runs the shared preflight for the Azure
// CalculateRefund endpoint: input + auth + session, load + authorize the
// purchase, enforce provider==azure and the 1h-safety-margin window check, and
// parse the reservation order/ID from the ARM path. Extracted to keep
// calculateAzureRevoke under the cyclomatic-complexity limit. Returns the loaded
// record plus the parsed orderID, reservationID, and commitment count.
func (h *Handler) validateAzureRevokeRequest(ctx context.Context, req *events.LambdaFunctionURLRequest, purchaseID string) (*config.PurchaseHistoryRecord, string, string, int, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	if purchaseID == "" {
		return nil, "", "", 0, NewClientError(400, "purchase_id is required")
	}
	if h.auth == nil {
		return nil, "", "", 0, NewClientError(403, "authentication service not configured")
	}

	session, err := h.requireSession(ctx, req)
	if err != nil {
		return nil, "", "", 0, err
	}

	record, err := h.config.GetPurchaseHistoryByPurchaseID(ctx, purchaseID)
	if err != nil {
		return nil, "", "", 0, fmt.Errorf("revoke/calculate: load purchase %s: %w", purchaseID, err)
	}
	if record == nil {
		return nil, "", "", 0, NewClientError(404, "purchase not found")
	}

	err = h.authorizeSessionRevoke(ctx, session, record)
	if err != nil {
		return nil, "", "", 0, err
	}

	orderID, reservationID, err := azureRevokeWindowAndIDs(record)
	if err != nil {
		return nil, "", "", 0, err
	}
	return record, orderID, reservationID, record.Count, nil
}

// azureRevokeWindowAndIDs enforces provider==azure and the 1h-safety-margin
// window check, then parses the reservation order/ID from the ARM path.
// Extracted from validateAzureRevokeRequest to keep both under the cyclomatic-
// complexity limit. Returns 422 ClientErrors for every reject case.
func azureRevokeWindowAndIDs(record *config.PurchaseHistoryRecord) (string, string, error) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	if record.Provider != "azure" {
		return "", "", NewClientError(422, fmt.Sprintf("provider %q does not support refund calculation", record.Provider))
	}

	windowClosesAt := record.Timestamp.AddDate(0, 0, AzureRevocationWindowDays)
	if record.RevocationWindowClosesAt != nil {
		windowClosesAt = *record.RevocationWindowClosesAt
	}
	// Apply the 1h safety margin so we stop offering the button before Azure's
	// hard edge (clock-skew protection, issue #290 Finding #3).
	if time.Now().UTC().After(windowClosesAt.Add(-azureRefundSafetyMargin)) {
		return "", "", NewClientError(422, fmt.Sprintf(
			"Azure reservation return window closed at %s (%d days after purchase)",
			windowClosesAt.Format(time.RFC3339), AzureRevocationWindowDays,
		))
	}

	orderID, reservationID, err := parseAzureReservationIDs(record.PurchaseID)
	if err != nil {
		return "", "", NewClientError(422, "cannot determine Azure reservation order ID from purchase record; contact Azure Support to request a refund")
	}
	if orderID == "" || reservationID == "" {
		return "", "", NewClientError(422, "cannot determine Azure reservation ID from purchase record; contact Azure Support to request a refund")
	}
	return orderID, reservationID, nil
}

// extractAzureRefundQuote pulls the refund amount and currency out of a
// CalculateRefund response, guarding every nil pointer in the chain. A missing
// amount is returned as nil and a missing currency as "", never as zero, so
// callers can tell "Azure quoted nothing" from "Azure quoted 0".
func extractAzureRefundQuote(resp armreservations.CalculateRefundClientPostResponse) (*float64, string) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	if resp.Properties == nil || resp.Properties.BillingRefundAmount == nil {
		return nil, ""
	}
	price := resp.Properties.BillingRefundAmount
	var amount *float64
	if price.Amount != nil {
		v := *price.Amount
		amount = &v
	}
	var currency string
	if price.CurrencyCode != nil {
		currency = *price.CurrencyCode
	}
	return amount, currency
}

// revokeAzurePurchase handles Azure reservation returns via the Azure
// Reservations API (CalculateRefund + Return). The reservation order ID and
// reservation ID are parsed from the purchase_id ARM resource path stored at
// purchase time.
func (h *Handler) revokeAzurePurchase(ctx context.Context, record *config.PurchaseHistoryRecord, confirmed revokeConfirmBody) (any, error) {
	// Prefer the window stamped on the row at purchase time (single source of
	// truth, issue #290). Fall back to recomputing from Timestamp for legacy
	// rows written before the column was populated, so they remain revocable.
	windowClosesAt := record.Timestamp.AddDate(0, 0, AzureRevocationWindowDays)
	if record.RevocationWindowClosesAt != nil {
		windowClosesAt = *record.RevocationWindowClosesAt
	}
	// Apply the 1h safety margin so we stop accepting revoke requests before
	// Azure's hard edge (clock-skew protection, issue #290 Finding #3).
	if time.Now().UTC().After(windowClosesAt.Add(-azureRefundSafetyMargin)) {
		return nil, NewClientError(422, fmt.Sprintf(
			"Azure reservation return window closed at %s (%d days after purchase)",
			windowClosesAt.Format(time.RFC3339), AzureRevocationWindowDays,
		))
	}

	orderID, reservationID, err := parseAzureReservationIDs(record.PurchaseID)
	if err != nil {
		logging.Warnf("revoke azure: cannot parse reservation IDs from purchase_id %q: %v", record.PurchaseID, err)
		return nil, NewClientError(422, "cannot determine Azure reservation order ID from purchase record; contact Azure Support to request a refund")
	}

	calcClient, returnClient, err := h.buildAzureRevokeClients()
	if err != nil {
		return nil, fmt.Errorf("revoke azure: %w", err)
	}

	return h.callAzureReturn(ctx, calcClient, returnClient, record, orderID, reservationID, confirmed)
}
