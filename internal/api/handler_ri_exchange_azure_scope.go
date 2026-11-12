package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// requireAzureSubscriptionScope enforces the session's allowed_accounts
// scope (issue #1030) against the CloudAccount registered for
// subscriptionID, the same per-account gate the sibling /ri-exchange
// endpoints apply. Without it, subscription_id is a caller-controlled
// pointer at any subscription in the tenant: a user scoped to one account
// could price, and with an otherwise-unconstrained execute:ri-exchange
// permission execute, an exchange against another account's subscription.
// The per-permission Constraints check does not cover this -- it only
// consults the permission's own AccountIDs, never the user's
// allowed_accounts.
//
// Returns errNotFound (404, not 403) when a scoped session names a
// subscription outside its scope, including one with no registered account
// at all, matching requireAccountAccess: a user must not be able to probe
// which subscriptions exist outside their scope.
//
// Unrestricted / admin sessions short-circuit before the account fetch,
// mirroring requireExecutionAccess.
func (h *Handler) requireAzureSubscriptionScope(ctx context.Context, session *Session, subscriptionID string) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	account, err := h.lookupAzureAccount(ctx, subscriptionID)
	if err != nil {
		return err
	}
	return azureAccountInScope(allowed, account)
}

// requireAzureAccountScope is requireAzureSubscriptionScope for a caller
// that has already resolved the subscription's CloudAccount (nil when none
// is registered), with the same errNotFound denials.
func (h *Handler) requireAzureAccountScope(ctx context.Context, session *Session, account *config.CloudAccount) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	return azureAccountInScope(allowed, account)
}

// azureAccountInScope refuses, with errNotFound, an unregistered account and
// one outside the session's scope alike, so the two are indistinguishable.
func azureAccountInScope(allowed auth.AccountScope, account *config.CloudAccount) error {
	if account == nil || !allowed.Allows(account.ID, account.Name) {
		return errNotFound
	}
	return nil
}

// lookupAzureAccount returns the CloudAccount registered for subscriptionID,
// or nil when none is.
func (h *Handler) lookupAzureAccount(ctx context.Context, subscriptionID string) (*config.CloudAccount, error) {
	account, err := h.config.GetCloudAccountByExternalID(ctx, "azure", subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve cloud account scope: %w", err)
	}
	return account, nil
}

// requireAzureSourceOwnership refuses any source reservation that is not
// paid for by the authorized subscription (issue #1527).
//
// Every other gate on these endpoints constrains the DESTINATION of an
// exchange: requireAzureSubscriptionScope and the execute:ri-exchange
// AccountIDs constraint both key off subscription_id, and
// toAzureExchangeTargets forces each target's billing scope to that same
// subscription. The sources were unconstrained. That matters because Azure
// reservation orders are tenant-scoped, not subscription-scoped --
// ListExchangeableReservations enumerates the whole tenant precisely
// because "the Azure Capacity exchange API operates on reservation order
// IDs which span subscriptions". So a caller authorized for subscription A
// could name subscription B's reservation IDs and hand B's commitments
// back, with the replacement purchased into A's billing scope. Azure RBAC
// on the reservation order was the only backstop.
//
// The check uses each reservation's own BillingScopeID -- the subscription
// Azure charges for it, and the scope an exchange refunds it to. That is
// the correct discriminator even for AppliedScopeType == Shared, which
// governs which subscriptions receive the discount rather than which one
// paid; an AppliedScopes-based check would pass for nearly every
// reservation and be security theater.
//
// Fails closed on every uncertainty: a reservation absent from the listing,
// or one Azure reports without a billing scope, is refused rather than
// allowed. Denials deliberately do not distinguish "does not exist" from
// "belongs to someone else", so this cannot be used to enumerate another
// subscription's reservation IDs (same posture as
// requireAzureSubscriptionScope).
func requireAzureSourceOwnership(owned []azureExchangeableReservation, sources []AzureExchangeSourceBody, subscriptionID string) error {
	scope := azureBillingScopeID(subscriptionID)
	byID := make(map[string]string, len(owned))
	for i := range owned {
		byID[strings.ToLower(owned[i].ReservationID)] = owned[i].BillingScopeID
	}
	for i, s := range sources {
		billingScope, found := byID[strings.ToLower(s.ReservationID)]
		if !found || billingScope == "" || !strings.EqualFold(billingScope, scope) {
			return NewClientError(403, fmt.Sprintf(
				"sources[%d].reservation_id is not a reservation billed to subscription %q; an exchange may only hand back reservations that subscription paid for",
				i, subscriptionID))
		}
	}
	return nil
}

// checkAzureSourceOwnership fetches the caller's visible reservations and
// applies requireAzureSourceOwnership. Split from the pure check so the
// authorization rule itself is testable without a client, and so both the
// pricing and execute endpoints share one code path.
func checkAzureSourceOwnership(ctx context.Context, client azureExchangeClient, sources []AzureExchangeSourceBody, subscriptionID string) ([]azureExchangeableReservation, error) {
	owned, err := listOwnedAzureReservations(ctx, client)
	if err != nil {
		return nil, err
	}
	if err := requireAzureSourceOwnership(owned, sources, subscriptionID); err != nil {
		return nil, err
	}
	return toAzureExchangeSources(sources, owned), nil
}

// listOwnedAzureReservations fetches the tenant-wide reservation listing that
// both source-side gates consult: requireAzureSourceOwnership (which
// subscription paid for each source) and exchangeRegions (where each source
// lives). Extracted so the execute path can fetch it once and feed both,
// rather than listing twice and risking the two gates disagreeing.
//
// Fails closed: without the listing we can establish neither ownership nor
// region, and permitting the exchange would restore the very gaps those
// checks exist to close. 502 rather than 500 because the upstream dependency,
// not this service, is what failed.
func listOwnedAzureReservations(ctx context.Context, client azureExchangeClient) ([]azureExchangeableReservation, error) {
	owned, err := client.ListExchangeableReservations(ctx)
	if err != nil {
		logging.Errorf("azure exchange source ownership lookup failed: %v", err)
		return nil, NewClientError(502, "could not verify which subscription owns the requested reservations; refusing to proceed")
	}
	return owned, nil
}
