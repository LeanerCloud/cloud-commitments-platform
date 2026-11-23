package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armreservations "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
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
