package api

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// azureMaxPurchaseAmountCurrency is the currency the execute:ri-exchange
// permission's MaxPurchaseAmount constraint is denominated in, matching the
// AWS execute:ri-exchange precedent (which takes max_payment_due_usd). There
// is no FX conversion available here, so a non-USD exchange's raw amount can
// never be safely compared against a USD-denominated cap -- see
// checkAzureExecuteConstraints.
const azureMaxPurchaseAmountCurrency = "USD"

// authorizeAzureExchangeExecution builds the Azure exchange client for the
// request's subscription, refuses sources the subscription does not own
// (issue #1527), and enforces the per-permission Constraints configured on
// execute:ri-exchange (SEC-01, issue #1141). Extracted from
// executeAzureExchange to keep that function under the gocyclo limit.
//
// Order matters, and all three gates run before any pricing or commit call:
//
//   - The tenant listing is fetched before the constraint check because the
//     Regions dimension cannot be assembled without knowing where the sources
//     live (exchangeRegions). An unavailable listing therefore refuses with
//     502 ahead of any constraint denial. The caller has already cleared
//     requirePermission("execute", "ri-exchange") and the allowed_accounts
//     scope for this subscription by then, so the read-only listing call is
//     within what they are authorized to trigger.
//
//   - Ownership is checked before the constraint check so the two cannot form
//     an enumeration oracle. If the constraint check ran first, a caller
//     scoped to subscription A and permitted only in eastus would get
//     distinguishable answers for a reservation id they do not own: an id
//     that exists in eastus (owned by subscription B) would clear the Regions
//     dimension and be refused by the ownership gate, while an id that does
//     not exist -- or lives in an unpermitted region -- would be refused by
//     the constraint check with a different message. That difference confirms
//     "this reservation id exists, in one of my permitted regions, in a
//     subscription I am not scoped to". requireAzureSourceOwnership
//     deliberately makes its own denials indistinguishable; running it first
//     keeps the whole path that way.
//
//     It also sharpens exchangeRegions: every source reaching it is
//     known-owned, so unknownRegionConstraint means only "owned, but Azure
//     reported no region" rather than doubling as "not yours" or "not real".
func (h *Handler) authorizeAzureExchangeExecution(ctx context.Context, session *Session, body AzureExecuteExchangeRequestBody, maxRat *big.Rat) (client azureExchangeClient, sources []azureExchangeableReservation, accountID string, err error) {
	// The subscription's CloudAccount is resolved once and that one row
	// feeds the scope check, the client's credentials and the constraint
	// AccountIDs (issue #311), so the three cannot see different rows.
	account, err := h.lookupAzureAccount(ctx, body.SubscriptionID)
	if err != nil {
		return nil, nil, "", err
	}

	// Scope check MUST precede building the client (mirrors
	// getAzureCompatibleOfferings): otherwise an unregistered subscription
	// (distinguishable 404: "no Azure account registered...") and a
	// registered-but-out-of-scope one (generic errNotFound) would leak an
	// enumeration signal to a scoped caller about which subscriptions exist,
	// and credentials for an out-of-scope account could be resolved before
	// the denial.
	if scopeErr := h.requireAzureAccountScope(ctx, session, account); scopeErr != nil {
		return nil, nil, "", scopeErr
	}

	client, err = h.azureExchangeClientForAccount(ctx, body.SubscriptionID, account)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to build Azure exchange client: %w", err)
	}
	if client == nil {
		return nil, nil, "", NewClientError(404, fmt.Sprintf("no Azure account registered for subscription %q", body.SubscriptionID))
	}

	owned, err := listOwnedAzureReservations(ctx, client)
	if err != nil {
		return nil, nil, "", err
	}

	if ownErr := requireAzureSourceOwnership(owned, body.Sources, body.SubscriptionID); ownErr != nil {
		return nil, nil, "", ownErr
	}

	accountID = azureExchangeAccountID(account)
	if err := h.checkAzureExecuteConstraints(ctx, session, body, accountID, maxRat, exchangeRegions(body.Targets, body.Sources, owned)); err != nil {
		return nil, nil, "", err
	}
	return client, toAzureExchangeSources(body.Sources, owned), accountID, nil
}

// azureExchangeAccountID returns the resolved CloudAccount's ID, or
// unattributedAccountConstraint when no account is registered (so an
// AccountIDs-constrained permission still fails closed against an
// unattributed request). Only the injected azureExchangeFactory path gets
// this far with a nil account: production refuses it with a 404 earlier.
func azureExchangeAccountID(account *config.CloudAccount) string {
	if account != nil {
		return account.ID
	}
	return unattributedAccountConstraint
}

// checkAzureExecuteConstraints enforces the execute:ri-exchange permission
// Constraints (SEC-01, issue #1141): AccountIDs from the resolved
// CloudAccount, Providers/Services fixed to azure/compute, Regions covering
// every region the exchange touches (see exchangeRegions -- both the target
// locations and the source reservations' own regions), and MaxPurchaseAmount
// from the caller's cap.
//
// MaxPurchaseAmount is USD-denominated (azureMaxPurchaseAmountCurrency) with
// no FX conversion available. A non-USD request's raw amount is therefore
// never compared directly against the cap -- doing so would let a large
// non-USD amount (e.g. 1000 KWD, worth far more than 1000 USD) clear a cap
// meant to bound USD spend. Instead, a non-USD request is checked with an
// unmatchable sentinel amount (math.MaxFloat64): this denies the request if
// the granting permission carries ANY MaxPurchaseAmount constraint (fail
// closed on a cap this code cannot safely evaluate) while still allowing it
// through when the permission has no amount constraint at all -- callers
// without a spend cap are not penalized for using a non-USD subscription.
//
// When the sentinel check fails, a second call with the amount dimension
// neutralized (MaxPurchaseAmount: 0, which matchPurchaseAmountConstraint
// treats as satisfied by any well-formed cap; a malformed negative cap still
// denies it, issue #83) disambiguates the cause: if that second call
// still fails, some other dimension (account/provider/service/region) is
// the real reason and its error is returned unchanged; otherwise the amount
// constraint was specifically the blocker and a currency-specific 403 is
// returned instead of the generic constraint-denied message.
func (h *Handler) checkAzureExecuteConstraints(ctx context.Context, session *Session, body AzureExecuteExchangeRequestBody, accountID string, maxRat *big.Rat, regions []string) error {
	base := auth.PermissionConstraints{
		AccountIDs: []string{accountID},
		Providers:  []string{string(common.ProviderAzure)},
		Services:   []string{string(common.ServiceCompute)},
		Regions:    regions,
	}

	isUSD := strings.EqualFold(body.Currency, azureMaxPurchaseAmountCurrency)
	attempt := base
	if isUSD {
		maxPayment, _ := maxRat.Float64()
		attempt.MaxPurchaseAmount = maxPayment
	} else {
		attempt.MaxPurchaseAmount = math.MaxFloat64
	}

	err := h.requirePermissionConstraints(ctx, session, "execute", "ri-exchange", []auth.PermissionConstraints{attempt})
	if err == nil || isUSD {
		return err
	}

	// Non-USD and denied: isolate whether the amount dimension was
	// specifically the cause.
	withoutAmount := base
	withoutAmount.MaxPurchaseAmount = 0
	if otherErr := h.requirePermissionConstraints(ctx, session, "execute", "ri-exchange", []auth.PermissionConstraints{withoutAmount}); otherErr != nil {
		return otherErr
	}
	return NewClientError(403, fmt.Sprintf(
		"your execute:ri-exchange permission has a spend-cap (MaxPurchaseAmount) constraint, which is USD-denominated and cannot be safely enforced against a %s exchange; use a USD-denominated request or ask an administrator to remove the constraint",
		body.Currency))
}
