package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
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

// checkAzureExchangeMoneyGuardrails enforces the money-path guardrails
// against a freshly-obtained CalculateExchange preview, before its
// SessionID is allowed to reach ExecuteExchange: non-empty policy errors
// block execution, a nil NetPayable is refused rather than treated as free,
// a currency mismatch blocks execution, and NetPayable exceeding the cap
// blocks execution. Extracted from executeAzureExchange to keep that
// function under the gocyclo limit.
//
// A nil preview is itself refused rather than dereferenced: the current
// azureExchangeClient.CalculateExchange contract never returns (nil, nil,
// nil), but a future implementation of the interface making that mistake
// must not panic this handler.
func checkAzureExchangeMoneyGuardrails(preview *azureExchangePreview, maxRat *big.Rat, currency string) error {
	if preview == nil {
		return fmt.Errorf("internal error: CalculateExchange returned a nil preview")
	}
	if len(preview.PolicyErrors) > 0 {
		logging.Warnf("ri-exchange: Azure policy refused the exchange: %s", strings.Join(preview.PolicyErrors, "; "))
		return NewClientError(422, azurePolicyRefusedMessage)
	}
	if preview.NetPayable == nil {
		return NewClientError(422, "Azure did not return a net payable amount; refusing to execute")
	}
	// Case-insensitive to match the isUSD test in checkAzureExecuteConstraints:
	// a request of "usd" must not clear the USD-denominated cap check there
	// and then be rejected here as a mismatch against Azure's "USD".
	if !strings.EqualFold(preview.NetPayableCurrency, currency) {
		return NewClientError(422, fmt.Sprintf("quoted currency %q does not match requested currency %q", preview.NetPayableCurrency, currency))
	}
	netPayableRat := new(big.Rat).SetFloat64(*preview.NetPayable)
	if netPayableRat == nil {
		return fmt.Errorf("internal error: quoted net payable %v is not a finite number", *preview.NetPayable)
	}
	if netPayableRat.Cmp(maxRat) > 0 {
		return NewClientError(422, fmt.Sprintf("quoted net payable %s %s exceeds max_payment_due %s %s",
			netPayableRat.FloatString(2), currency, maxRat.FloatString(2), currency))
	}
	return nil
}

// parseAzureExecuteRequest decodes and validates an execute request body and
// parses its spend cap into an exact rational.
//
// Extracted from executeAzureExchange purely to keep that function within
// the project's gocyclo limit once the issue #1527 source-ownership gate was
// added; it makes no decisions of its own beyond returning the same errors
// inline code did.
func parseAzureExecuteRequest(rawBody string) (AzureExecuteExchangeRequestBody, *big.Rat, error) {
	var body AzureExecuteExchangeRequestBody
	if err := json.Unmarshal([]byte(rawBody), &body); err != nil {
		return body, nil, NewClientError(400, "invalid request body")
	}
	if err := validateAzureExecuteBody(body); err != nil {
		return body, nil, err
	}
	maxRat, err := exchange.ParseDecimalRat(body.MaxPaymentDue)
	if err != nil {
		return body, nil, NewClientError(400, fmt.Sprintf("invalid max_payment_due: %v", err))
	}
	return body, maxRat, nil
}

// executeAzureExchange executes an Azure RI exchange with mandatory
// spend-cap and currency guardrails. Requires "execute:ri-exchange"
// (deliberately separate from execute:purchases), mirroring the AWS
// executeExchange handler: RI exchanges are financially irreversible once
// submitted.
//
// Unlike a design that executes a client-supplied session_id, this handler
// never trusts the caller's own pricing: it re-runs CalculateExchange itself
// against the caller's sources/targets, validates the FRESH quote against
// every guardrail in checkAzureExchangeMoneyGuardrails, and only then calls
// ExecuteExchange with the SessionID *that fresh call returned*. A
// client-supplied or stale session would bypass every guardrail below, so
// the server always re-quotes immediately before committing.
//
// POST /api/ri-exchange/azure-instances/exchange.
func (h *Handler) executeAzureExchange(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "execute", "ri-exchange")
	if err != nil {
		return nil, err
	}

	body, maxRat, err := parseAzureExecuteRequest(req.Body)
	if err != nil {
		return nil, err
	}

	// authorizeAzureExchangeExecution applies every gate: allowed_accounts
	// scope, source ownership (issue #1527) and the execute:ri-exchange
	// Constraints, all before the pricing call below.
	client, sources, accountID, err := h.authorizeAzureExchangeExecution(ctx, session, body, maxRat)
	if err != nil {
		return nil, err
	}

	err = h.requireNoDirectExchangeUnderFourEyes(ctx)
	if err != nil {
		return nil, err
	}

	targets, err := toAzureExchangeTargets(body.Targets, body.SubscriptionID)
	if err != nil {
		return nil, err
	}

	preview, _, err := client.CalculateExchange(ctx, sources, targets)
	if err != nil {
		logging.Errorf("azure exchange re-quote failed: %v", err)
		return nil, mapAzureExchangeError("failed to price the exchange before execution", err)
	}

	err = checkAzureExchangeMoneyGuardrails(preview, maxRat, body.Currency)
	if err != nil {
		return nil, err
	}

	// Audit trail (#82), then submit-time idempotency (#1642). The re-quote
	// above mints a FRESH session on every request, so Azure's own
	// session-level replay protection cannot see two POSTs of one logical
	// exchange as duplicates: without the claim a client that times out
	// mid-LRO and retries commits the exchange twice, each half individually
	// under the cap. Taken here, last, so no gate rejection ever leaves a
	// claim behind.
	// The execution window is measured from just before the audit save (#1840).
	deadline := time.Now().Add(h.exchangeExecutionTimeout())
	auditID, err := h.beginDirectExchange(ctx,
		azureDirectExchangeAudit(session, accountID, body, maxRat, preview),
		azureExchangeIdempotencyKey(body), deadline)
	if err != nil {
		return nil, err
	}

	// Past the claim the exchange may commit at Azure; a cancelled request must
	// not abort the submit or its long-running-operation poll (#790).
	execCtx, cancelExec := ctxutil.DetachedWithDeadline(ctx, deadline)
	defer cancelExec()
	result, err := client.ExecuteExchange(execCtx, preview.SessionID)
	if err != nil {
		logging.Errorf("azure exchange execution failed: %v", err)
		// Non-4xx failures here are ambiguous: BeginPost may already have
		// submitted the exchange, and exhausting the riExchangeExecutionTimeout
		// budget mid-poll looks identical to a call that never reached Azure
		// (a request cancel no longer reaches execCtx). Say so rather than
		// reporting a flat failure that invites the retry this claim now
		// refuses (#1642). The audit row records the same ambiguity.
		mapped := mapAzureExchangeError(
			"exchange execution failed and may already have been submitted to Azure; "+
				"verify the reservation state in the Azure portal before retrying", err)
		h.finalizeDirectExchange(ctx, auditID, azureDirectExchangeOutcome(nil, err, mapped))
		return nil, mapped
	}
	h.finalizeDirectExchange(ctx, auditID, azureDirectExchangeOutcome(result, nil, nil))

	logging.Infof("azure ri-exchange executed: subscription=%s session=%s status=%s", body.SubscriptionID, result.SessionID, result.Status)

	return &AzureExecuteExchangeResponse{
		SessionID:          result.SessionID,
		Status:             result.Status,
		NetPayable:         result.NetPayable,
		NetPayableCurrency: result.NetPayableCurrency,
		RefundsTotal:       preview.RefundsTotal,
		PurchasesTotal:     preview.PurchasesTotal,
	}, nil
}
