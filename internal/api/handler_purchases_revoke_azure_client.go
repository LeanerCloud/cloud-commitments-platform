package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armreservations "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

func (h *Handler) getAzureRevokeFactory() azureRevokeClientFactory {
	if h.azureRevokeFactory != nil {
		return *h.azureRevokeFactory
	}
	return defaultAzureRevokeFactory(nil)
}

// defaultAzureRevokeFactory builds the production clients. returnOpts is nil
// in production; tests point it at a local server to exercise the real Return
// client's pipeline, including returnAttemptPolicy.
func defaultAzureRevokeFactory(returnOpts *arm.ClientOptions) azureRevokeClientFactory {
	return azureRevokeClientFactory{
		newCredential: func() (azcore.TokenCredential, error) {
			return azidentity.NewDefaultAzureCredential(nil)
		},
		newCalculateRefundClient: func(cred azcore.TokenCredential) (azureCalculateRefundClient, error) {
			return armreservations.NewCalculateRefundClient(cred, nil)
		},
		newReturnClient: func(cred azcore.TokenCredential) (azureReturnClient, error) {
			return newAzureReturnClient(cred, returnOpts)
		},
	}
}

func (h *Handler) buildAzureCalculateRefundClient() (azureCalculateRefundClient, error) {
	factory := h.getAzureRevokeFactory()
	cred, err := factory.newCredential()
	if err != nil {
		return nil, fmt.Errorf("obtain credential: %w", err)
	}
	calcClient, err := factory.newCalculateRefundClient(cred)
	if err != nil {
		return nil, fmt.Errorf("create calculate-refund client: %w", err)
	}
	return calcClient, nil
}

// buildAzureRevokeClients constructs both clients from one credential because
// the revoke flow calls CalculateRefund immediately before Return.
func (h *Handler) buildAzureRevokeClients() (azureCalculateRefundClient, azureReturnClient, error) {
	factory := h.getAzureRevokeFactory()
	cred, err := factory.newCredential()
	if err != nil {
		return nil, nil, fmt.Errorf("obtain credential: %w", err)
	}
	calcClient, err := factory.newCalculateRefundClient(cred)
	if err != nil {
		return nil, nil, fmt.Errorf("create calculate-refund client: %w", err)
	}
	returnClient, err := factory.newReturnClient(cred)
	if err != nil {
		return nil, nil, fmt.Errorf("create return client: %w", err)
	}
	return calcClient, returnClient, nil
}

// callAzureReturn executes the two-step Azure reservation return:
// CalculateRefund (to get the session ID and quoted amount) followed by Return.
// Extracted from revokeAzurePurchase to allow test injection of the two clients.
//
// confirmed: the quote the user consented to after the quote step
// (GET /revoke/calculate). It is mandatory; see checkConfirmedRefund.
func (h *Handler) callAzureReturn(
	ctx context.Context,
	calcClient azureCalculateRefundClient,
	returnClient azureReturnClient,
	record *config.PurchaseHistoryRecord,
	orderID, reservationID string,
	confirmed revokeConfirmBody,
) (any, error) {
	// Guard against an order-only ARM path (no /reservations/{id} segment),
	// which parseAzureReservationIDs returns with an empty reservationID.
	// Submitting a Return for an empty reservation would either fail opaquely
	// or, worse, be misinterpreted by the API; reject it up front so the
	// caller gets a clear, actionable error instead.
	if orderID == "" || reservationID == "" {
		return nil, NewClientError(422, "cannot determine Azure reservation ID from purchase record; contact Azure Support to request a refund")
	}
	if confirmed.ExpectedRefundAmount == nil || strings.TrimSpace(confirmed.ExpectedRefundCurrency) == "" {
		return nil, NewClientError(400, "expected_refund_amount and expected_refund_currency are required; fetch a quote from GET /api/purchases/{id}/revoke/calculate and confirm it")
	}

	// Step 1: CalculateRefund -> sessionID + quoted amount (TOCTOU check).
	quantity := int32(record.Count) // #nosec G115 -- Azure reservation count validated at purchase; bounded by API limits (<<math.MaxInt32) //nolint:gosec
	sessionID, calcRefundAmount, calcRefundCurrency, err := h.azureCalculateRefund(ctx, calcClient, record.PurchaseID, orderID, reservationID, quantity)
	if err != nil {
		return nil, err
	}

	if quoteErr := checkConfirmedRefund(confirmed, calcRefundAmount, calcRefundCurrency); quoteErr != nil {
		return nil, quoteErr
	}

	// The claim stays on the request context: a request canceled before it
	// takes no claim and posts no Return.
	if stop, claimErr := h.claimRevocationInFlight(ctx, record); stop != nil || claimErr != nil {
		return stop, claimErr
	}

	// Step 2: Return (post the actual refund request).
	if attempts, err := postAzureReturn(ctx, returnClient, orderID, reservationID, sessionID, quantity); err != nil {
		return h.handleAzureReturnError(ctx, record, attempts, err)
	}

	return h.persistAzureRevocation(ctx, record, calcRefundAmount, calcRefundCurrency)
}

// postAzureReturn posts the Return for a reservation whose in-flight claim the
// caller holds. Past the claim Azure may refund whether or not the request is
// still alive, so the call runs on a context that keeps the request's values
// but not its cancellation, bounded by revokeReturnTimeout (issue #1527), as
// the RI exchange handler does once its claim is held (#790). A request
// canceled mid-Return therefore still learns that Azure accepted it and
// records it.
//
// It also reports how many HTTP requests the Return client sent, counted by
// returnAttemptPolicy, so a failure can be judged definite or uncertain
// (issue #1529).
func postAzureReturn(reqCtx context.Context, returnClient azureReturnClient, orderID, reservationID, sessionID string, quantity int32) (int32, error) {
	ctx, cancel := ctxutil.DetachedWithTimeout(reqCtx, revokeReturnTimeout)
	defer cancel()
	ctx, sends := withReturnAttemptCounter(ctx)
	_, err := returnClient.Post(ctx, orderID, armreservations.RefundRequest{
		Properties: &armreservations.RefundRequestProperties{
			ReservationToReturn: &armreservations.ReservationToReturn{
				ReservationID: &reservationID,
				Quantity:      &quantity,
			},
			SessionID:    &sessionID,
			ReturnReason: toPtr("Revoked via CUDly within free-cancel window"),
			Scope:        toPtr("Reservation"),
		},
	}, nil)
	return sends.Load(), err
}

// parseAzureReservationIDs extracts the reservation order ID and reservation ID
// from an Azure ARM resource path. The purchase_id is stored as the ARM
// resource ID at purchase time.
//
// Accepted formats (case-insensitive path segments):
//
//	/subscriptions/{sub}/providers/Microsoft.Capacity/reservationOrders/{orderID}/reservations/{resID}
//	/providers/Microsoft.Capacity/reservationOrders/{orderID}/reservations/{resID}
//	/providers/Microsoft.Capacity/reservationOrders/{orderID}
func parseAzureReservationIDs(purchaseID string) (orderID, reservationID string, err error) {
	lower := strings.ToLower(purchaseID)
	const orderKey = "reservationorders/"

	orderIdx := strings.Index(lower, orderKey)
	if orderIdx < 0 {
		return "", "", fmt.Errorf("no reservationOrders segment in %q", purchaseID)
	}
	afterOrder := purchaseID[orderIdx+len(orderKey):]

	resIdx := strings.Index(strings.ToLower(afterOrder), "/reservations/")
	if resIdx < 0 {
		// Order-only path.
		return strings.TrimRight(afterOrder, "/"), "", nil
	}
	orderID = afterOrder[:resIdx]
	reservationID = afterOrder[resIdx+len("/reservations/"):]
	if sl := strings.Index(reservationID, "/"); sl >= 0 {
		reservationID = reservationID[:sl]
	}
	return orderID, reservationID, nil
}

// isAzureClientError reports whether err represents a 4xx (client-side) Azure
// API rejection that the frontend should see as a user-actionable error rather
// than an internal server error.
//
// The check uses typed error inspection (errors.As to *azcore.ResponseError)
// rather than substring matching on err.Error(). The substring approach had two
// failure modes:
//  1. False positives: a network timeout whose message happens to contain "400"
//     or "404" would be misclassified as a client error, hiding transient infra
//     problems from the operator.
//  2. False negatives: Azure may return refund-policy errors with HTTP status
//     codes we did not enumerate as string literals (e.g. 403, 405).
//
// The typed approach classifies exactly the HTTP status codes Azure uses for
// policy violations and bad requests; all other errors (transport errors,
// 5xx, unknown error types) correctly classify as server-side.
func isAzureClientError(err error) bool {
	if err == nil {
		return false
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case 400, 403, 404, 405, 409, 422:
			return true
		}
	}
	return false
}

// azureRefundRejected is the 400 for an Azure refund step the provider
// refused: a category message and the purchase ID, never Azure's own text,
// which carries subscription URLs and request IDs. The raw error is logged.
func azureRefundRejected(step, purchaseID string, err error) error {
	logging.Warnf("revoke azure: %s for purchase %s: %v", step, purchaseID, err)
	_, msg := purchase.SummarizeProviderError(err)
	return NewClientError(400, fmt.Sprintf("%s: %s (reference: purchase %s)", step, msg, purchaseID))
}

// isAzureWindowEdgeError reports whether err is an Azure RefundPolicyViolated
// rejection from the Return API. This specific error code is returned when the
// reservation's 7-day return window has closed (either because the request
// arrived just after expiry due to clock skew, or because a partial return
// was already submitted). It is distinct from general client errors because
// the appropriate HTTP response is 422 with code AZURE_WINDOW_EDGE rather
// than the generic 400 "Azure refund rejected" path.
func isAzureWindowEdgeError(err error) bool {
	if err == nil {
		return false
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.ErrorCode == "RefundPolicyViolated"
	}
	return false
}
