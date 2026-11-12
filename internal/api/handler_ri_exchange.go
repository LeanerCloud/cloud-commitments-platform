package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/smithy-go"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// filterAzureReservationsBySubscription narrows a tenant-wide reservation
// listing down to the rows billed to subscriptionID, using the same
// BillingScopeID discriminator requireAzureSourceOwnership keys on (issue
// #1527). Reservations with no BillingScopeID, or one billed to a different
// subscription, are dropped.
//
// Called only after requireAzureSubscriptionScope has cleared the caller for
// subscriptionID; a reservation billed elsewhere could never be named as a
// valid exchange source against this subscription anyway
// (requireAzureSourceOwnership refuses cross-subscription sources), so
// returning the wider tenant listing here would just be the enumeration leak
// issue #1656 describes.
func filterAzureReservationsBySubscription(reservations []azureExchangeableReservation, subscriptionID string) []azureExchangeableReservation {
	scope := azureBillingScopeID(subscriptionID)
	filtered := make([]azureExchangeableReservation, 0, len(reservations))
	for i := range reservations {
		if strings.EqualFold(reservations[i].BillingScopeID, scope) {
			filtered = append(filtered, reservations[i])
		}
	}
	return filtered
}

// filterAzureReservationsByScope narrows a tenant-wide Azure reservation
// listing down to the rows billed to a subscription the session's
// allowed_accounts scope covers. Mirrors filterDashboardRecommendations
// (handler_dashboard.go): unrestricted/admin sessions pass through
// unchanged; a restricted session keeps only the rows it can resolve back to
// one of its allowed CloudAccounts.
//
// Each reservation's BillingScopeID ("/subscriptions/{subscriptionID}") is
// the same ownership signal requireAzureSourceOwnership keys on (issue
// #1527) -- the subscription actually charged, which is the correct
// discriminator even for AppliedScopeType == Shared.
//
// Fails closed: a reservation with no BillingScopeID, or one that matches no
// registered Azure CloudAccount, is dropped rather than kept. Counts, not
// the dropped reservation/billing-scope identifiers, are safe to log --
// logging the identifiers would just relocate the disclosure this filter
// exists to prevent.
func (h *Handler) filterAzureReservationsByScope(ctx context.Context, session *Session, reservations []azureExchangeableReservation) ([]azureExchangeableReservation, error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		// client.ListExchangeableReservations may return a nil slice for zero
		// results; every other path here returns a non-nil empty slice, so
		// normalize here too rather than letting an admin session alone see
		// {"reservations": null}.
		if reservations == nil {
			reservations = []azureExchangeableReservation{}
		}
		return reservations, nil
	}

	provider := "azure"
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{Provider: &provider})
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts: %w", err)
	}

	filtered := filterReservationsByScopeIndex(reservations, azureScopeIndex(accounts), allowed)
	if dropped := len(reservations) - len(filtered); dropped > 0 {
		logging.Debugf("azure exchange listing: filtered %d of %d tenant-wide reservations outside session scope", dropped, len(reservations))
	}
	return filtered, nil
}

// azureScopeIndex builds a lookup from ARM billing scope (lower-cased) to the
// registered Azure CloudAccount that owns it, so filterAzureReservationsByScope
// can resolve each reservation's BillingScopeID straight to its CloudAccount
// without re-deriving azureBillingScopeID per row. Accounts with no
// ExternalID are skipped -- they cannot own any BillingScopeID.
//
// Split out of filterAzureReservationsByScope, together with
// filterReservationsByScopeIndex below, to stay under the project's gocyclo
// threshold (mirrors validateAzureOfferingsBody's precedent).
func azureScopeIndex(accounts []config.CloudAccount) map[string]config.CloudAccount {
	index := make(map[string]config.CloudAccount, len(accounts))
	for i := range accounts {
		if accounts[i].ExternalID == "" {
			continue
		}
		index[strings.ToLower(azureBillingScopeID(accounts[i].ExternalID))] = accounts[i]
	}
	return index
}

// filterReservationsByScopeIndex narrows reservations down to the rows whose
// BillingScopeID resolves, via scopeToAccount, to a CloudAccount the session
// scope allows (AccountScope.Allows). Fails closed: a reservation with no
// BillingScopeID, or one that resolves to no registered CloudAccount, is
// dropped rather than kept.
func filterReservationsByScopeIndex(reservations []azureExchangeableReservation, scopeToAccount map[string]config.CloudAccount, allowed auth.AccountScope) []azureExchangeableReservation {
	filtered := make([]azureExchangeableReservation, 0, len(reservations))
	for i := range reservations {
		r := reservations[i]
		if r.BillingScopeID == "" {
			continue
		}
		account, ok := scopeToAccount[strings.ToLower(r.BillingScopeID)]
		if !ok {
			continue
		}
		if allowed.Allows(account.ID, account.Name) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

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

// mapAzureExchangeError converts an error from an Azure RI exchange
// client-layer call to a ClientError with the appropriate HTTP status.
// A cloud-commitments-go exchange refusal (exchangeRefusalError) produces a
// 422 with the fixed exchangeRefusedMessage; Azure 4xx client faults (via
// isAzureClientError) produce a 400 with the fixed exchangeRejectedMessage;
// the provider text of both stays in the log. Any other error
// produces a 500 using the opMsg fallback -- the same contract
// mapAWSExchangeError applies to the AWS exchange endpoints.
func mapAzureExchangeError(opMsg string, err error) error {
	if refusal := exchangeRefusalError(err); refusal != nil {
		return refusal
	}
	if isAzureClientError(err) {
		logging.Warnf("ri-exchange: %s: provider rejected the request: %v", opMsg, err)
		return NewClientError(400, exchangeRejectedMessage)
	}
	return NewClientError(500, opMsg)
}

// validateTargets checks each entry in targets for a non-empty, UUID-shaped
// offering_id and a positive count. Extracted so both getExchangeQuote and
// validateExecuteExchangeBody share the same check without exceeding the
// gocyclo threshold.
//
// The count check mirrors pkg/exchange.validateTargets, which applies the same
// rule but only once ExecuteExchange is already running. Repeating it here
// moves the refusal ahead of the #1642 submit claim -- a request that can never
// commit must not leave a claim behind -- and turns what pkg/exchange would
// surface as an opaque 500 into a 400 naming the offending field.
func validateTargets(targets []ExchangeTargetBody) error {
	for i, t := range targets {
		if t.OfferingID == "" {
			return NewClientError(400, fmt.Sprintf("targets[%d].offering_id is required", i))
		}
		if !offeringIDPattern.MatchString(t.OfferingID) {
			return NewClientError(400, fmt.Sprintf(
				"targets[%d].offering_id %q does not look like an AWS offering UUID; "+
					"expected something like 4b2293b4-5fbc-4017-9c75-d5a9d3aa8c91 -- "+
					"did you paste an instance type by mistake?",
				i, t.OfferingID))
		}
		if t.Count < 1 {
			return NewClientError(400, fmt.Sprintf("targets[%d].count must be >= 1, got %d", i, t.Count))
		}
	}
	return nil
}

// validateExchangeRIIDs checks the source list and each id in it. Each id
// individually, not just the list length: a blank entry reaches the #1642
// submit fingerprint as an empty component, so two different blank spellings
// of one request would claim the same key.
func validateExchangeRIIDs(riIDs []string) error {
	if len(riIDs) == 0 {
		return NewClientError(400, "ri_ids is required")
	}
	for i, id := range riIDs {
		if strings.TrimSpace(id) == "" {
			return NewClientError(400, fmt.Sprintf("ri_ids[%d] is empty", i))
		}
	}
	return nil
}

// validateExchangeTargetsBody holds the source and target checks quote and
// execute share, so the quote cannot accept a request the library then fails
// with a 500. A whitespace-only target_offering_id counts as absent.
func validateExchangeTargetsBody(riIDs []string, targets []ExchangeTargetBody, offeringID string, count int32) error {
	if err := validateExchangeRIIDs(riIDs); err != nil {
		return err
	}
	legacy := strings.TrimSpace(offeringID) != ""
	if len(targets) == 0 && !legacy {
		return NewClientError(400, "either targets[] or target_offering_id is required")
	}
	if err := validateTargets(targets); err != nil {
		return err
	}
	// The legacy singleton's count, which validateTargets does not see.
	// Refuse before the submit claim rather than inside ExecuteExchange,
	// after it.
	if len(targets) == 0 && count < 1 {
		return NewClientError(400, fmt.Sprintf("target_count must be >= 1, got %d", count))
	}
	return nil
}

// getExchangeQuote gets a quote for an RI exchange.
func (h *Handler) getExchangeQuote(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, permErr := h.requirePermission(ctx, req, "view", "purchases")
	if permErr != nil {
		return nil, permErr
	}
	// A quote reveals the deployment account's RI ids and prices, so it takes
	// the same session-scope gate as executeExchange (issue #384).
	if err := h.requireReshapeAccountScope(ctx, session); err != nil {
		return nil, err
	}

	var body ExchangeQuoteRequestBody
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	if err := validateExchangeTargetsBody(body.RIIDs, body.Targets, body.TargetOfferingID, body.TargetCount); err != nil {
		return nil, err
	}

	// Resolve region from the AWS SDK chain when the caller omits it,
	// matching getReshapeRecommendations. Hardcoding us-east-1 would
	// return an incorrect quote for deployments in other regions.
	cfg, err := h.loadAWSConfigWithRegion(ctx, body.Region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	region := cfg.Region

	quote, err := h.awsExchangeQuoter()(ctx, exchange.ExchangeQuoteRequest{
		Region:           region,
		ReservedIDs:      body.RIIDs,
		Targets:          toExchangeTargets(body.Targets),
		TargetOfferingID: body.TargetOfferingID,
		TargetCount:      body.TargetCount,
	})
	if err != nil {
		logging.Errorf("exchange quote failed: %v", err)
		return nil, mapAWSExchangeError("exchange quote failed", err)
	}

	// Return the region the quote was priced in. Execute requires a region,
	// and callers must send this same one so the exchange runs where it was
	// priced.
	return ExchangeQuoteResponse{ExchangeQuoteSummary: quote, Region: region}, nil
}

// validateExecuteExchangeBody validates an unmarshalled request body
// and returns a caller-friendly 400 on the first offending field.
// Extracted from executeExchange to keep the handler below the
// cyclomatic-complexity threshold; every branch here becomes a
// separate test case so the logic stays inspectable.
func validateExecuteExchangeBody(body ExchangeExecuteRequestBody) error {
	if err := validateExchangeTargetsBody(body.RIIDs, body.Targets, body.TargetOfferingID, body.TargetCount); err != nil {
		return err
	}
	if body.MaxPaymentDueUSD == "" {
		return NewClientError(400, "max_payment_due_usd is required as a safety guardrail")
	}
	// Region is required on execute: RI exchanges are region-scoped and
	// financially irreversible. Silently defaulting to us-east-1 would
	// execute the exchange in the wrong region for deployments hosted
	// elsewhere, with no way to undo the operation.
	if body.Region == "" {
		return NewClientError(400, "region is required for execute; omitting it risks exchanging RIs in the wrong region")
	}
	return nil
}

// executeExchange executes an RI exchange with a spend-cap guardrail.
// Requires execute:ri-exchange (deliberately separate from execute:purchases)
// because RI exchanges are financially irreversible once submitted to AWS.
func (h *Handler) executeExchange(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "execute", "ri-exchange")
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope (issue #93). See
	// requireReshapeAccountScope for what this guards against.
	err = h.requireReshapeAccountScope(ctx, session)
	if err != nil {
		return nil, err
	}

	var body ExchangeExecuteRequestBody
	err = json.Unmarshal([]byte(req.Body), &body)
	if err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	err = validateExecuteExchangeBody(body)
	if err != nil {
		return nil, err
	}

	maxRat, err := exchange.ParseDecimalRat(body.MaxPaymentDueUSD)
	if err != nil {
		return nil, NewClientError(400, fmt.Sprintf("invalid max_payment_due_usd: %v", err))
	}

	region := body.Region

	cloudAccountID, err := h.authorizeAWSExchangeExecution(ctx, session, region, maxRat)
	if err != nil {
		return nil, err
	}

	// Audit trail (#82), then submit-time idempotency (#1642).
	// AcceptReservedInstancesExchangeQuote carries no ClientToken, so AWS will
	// happily accept the same exchange twice; a client that times out and
	// retries otherwise double-spends with each half individually under the
	// cap. Taken last, after every gate, so no rejected request leaves a
	// claim behind.
	//
	// The execution window is measured from just before the audit save (#1840):
	// one deadline bounds both the claim and the provider call.
	deadline := time.Now().Add(h.exchangeExecutionTimeout())
	auditID, err := h.beginDirectExchange(ctx,
		awsDirectExchangeAudit(session, cloudAccountID, body, maxRat),
		awsExchangeIdempotencyKey(cloudAccountID, body), deadline)
	if err != nil {
		return nil, err
	}

	// Past the claim the exchange may commit at AWS; a cancelled request must
	// not abort it midway (#790).
	execCtx, cancelExec := ctxutil.DetachedWithDeadline(ctx, deadline)
	defer cancelExec()
	exchangeID, quote, err := h.awsExchangeExecutor()(execCtx, exchange.ExchangeExecuteRequest{
		Region:           region,
		ReservedIDs:      body.RIIDs,
		Targets:          toExchangeTargets(body.Targets),
		TargetOfferingID: body.TargetOfferingID,
		TargetCount:      body.TargetCount,
		MaxPaymentDueUSD: maxRat,
	})
	if err != nil {
		logging.Errorf("exchange execution failed: %v", err)
		mapped := mapAWSExchangeError("exchange execution failed", err)
		if errors.Is(err, common.ErrOutcomeUnknown) {
			mapped = directExchangeOutcomeUnknown(body.RIIDs)
		}
		h.finalizeDirectExchange(ctx, auditID, awsDirectExchangeOutcome(exchangeID, quote, err))
		return nil, mapped
	}
	h.finalizeDirectExchange(ctx, auditID, awsDirectExchangeOutcome(exchangeID, quote, nil))

	return &ExchangeExecuteResponse{
		ExchangeID: exchangeID,
		Quote:      quote,
	}, nil
}

// awsExchangeClientFaultCodes is the set of AWS error codes that are
// documented client faults for RI exchange operations. These map to
// 4xx responses so the caller receives the fixed exchangeRejectedMessage
// (the AWS text stays in the log) and understands it was their input that
// was wrong. All other AWS
// errors remain 5xx (transient / server-side).
var awsExchangeClientFaultCodes = map[string]bool{
	"InvalidOfferingId":                   true,
	"InvalidParameter":                    true,
	"ValidationError":                     true,
	"InvalidReservedInstancesId.NotFound": true,
	"InvalidInstanceID.NotFound":          true,
}

// mapAWSExchangeError converts an AWS SDK error from an RI exchange
// operation to a ClientError with the appropriate HTTP status code.
// A cloud-commitments-go exchange refusal (exchangeRefusalError) produces
// a 422 with the fixed exchangeRefusedMessage. AWS 4xx client-fault errors
// produce a 400 with the fixed exchangeRejectedMessage; the provider text of
// both stays in the log. Any other error produces a 500
// (generic server failure) using the provided opMsg fallback.
func mapAWSExchangeError(opMsg string, err error) error {
	if refusal := exchangeRefusalError(err); refusal != nil {
		return refusal
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && awsExchangeClientFaultCodes[apiErr.ErrorCode()] {
		logging.Warnf("ri-exchange: %s: provider rejected the request: %v", opMsg, err)
		return NewClientError(400, exchangeRejectedMessage)
	}
	return NewClientError(500, opMsg)
}

// directExchangeOutcomeUnknown is the error for an accept whose response was
// lost: AWS may have exchanged the RIs, so the operator must check before
// retrying (#1050). The audit status is decided from the library's error,
// which must keep its wrapping (awsDirectFailureStatus), not from this status.
// It names only the caller's own RI ids, never provider text.
func directExchangeOutcomeUnknown(riIDs []string) error {
	return NewClientError(502, fmt.Sprintf("the exchange outcome is unknown: AWS may already have exchanged RI(s) %s; check them at AWS before retrying", strings.Join(riIDs, ", ")))
}

// exchangeRejectedMessage is the provider-neutral text for a client-fault
// rejection; the provider's own text stays in the log.
const exchangeRejectedMessage = "the exchange request was rejected as invalid: check the offering and count"

// azurePolicyRefusedMessage is the fixed 422 body for an Azure exchange policy
// refusal; the policy text stays in the log. The frontend keys its hint on the
// "Azure rejected this exchange" prefix (frontend/src/lib/azure-exchange.ts).
const azurePolicyRefusedMessage = "Azure rejected this exchange: it does not meet Azure's exchange policy"

// Response types

// ExchangeableAzureRIsResponse holds the list of Azure VM reservations that
// are eligible for the cross-SKU/cross-region exchange flow.
type ExchangeableAzureRIsResponse struct {
	Reservations []azureExchangeableReservation `json:"reservations"`
}

// ExchangeTargetBody is one entry in an ExchangeQuote/Execute request's
// `targets` array. Mirrors pkg/exchange.TargetConfig but with JSON tags
// shaped for the HTTP surface.
type ExchangeTargetBody struct {
	OfferingID string `json:"offering_id"`
	Count      int32  `json:"count"`
}

// ExchangeQuoteRequestBody is the request body for the quote endpoint.
// Callers may supply either the new `targets` array (preferred) or the
// legacy `target_offering_id` + `target_count` singleton fields. When
// both are present, `targets` wins. Exactly one of them must be
// provided (or the handler returns 400).
type ExchangeQuoteRequestBody struct {
	TargetOfferingID string               `json:"target_offering_id,omitempty"`
	Region           string               `json:"region,omitempty"`
	RIIDs            []string             `json:"ri_ids"`
	Targets          []ExchangeTargetBody `json:"targets,omitempty"`
	TargetCount      int32                `json:"target_count,omitempty"`
}

// ExchangeExecuteRequestBody is the request body for the execute endpoint.
// Same `targets` / legacy-alias semantics as ExchangeQuoteRequestBody.
// `max_payment_due_usd` is a TOTAL cap across all targets in the
// exchange — AWS returns a single aggregated PaymentDue so spend-cap
// checking naturally becomes a total when `targets[]` has multiple
// entries.
type ExchangeExecuteRequestBody struct {
	TargetOfferingID string               `json:"target_offering_id,omitempty"`
	MaxPaymentDueUSD string               `json:"max_payment_due_usd"`
	Region           string               `json:"region,omitempty"`
	RIIDs            []string             `json:"ri_ids"`
	Targets          []ExchangeTargetBody `json:"targets,omitempty"`
	TargetCount      int32                `json:"target_count,omitempty"`
}

// toExchangeTargets converts the HTTP-shaped targets into the
// pkg/exchange shape, preserving nil (not empty) when the caller used
// the legacy singleton fields so the pkg/exchange layer knows to fall
// back to them.
func toExchangeTargets(targets []ExchangeTargetBody) []exchange.TargetConfig {
	if len(targets) == 0 {
		return nil
	}
	out := make([]exchange.TargetConfig, 0, len(targets))
	for _, t := range targets {
		out = append(out, exchange.TargetConfig{OfferingID: t.OfferingID, Count: t.Count})
	}
	return out
}

// ExchangeExecuteResponse is the response from a successful exchange execution.
type ExchangeExecuteResponse struct {
	Quote      *exchange.ExchangeQuoteSummary `json:"quote"`
	ExchangeID string                         `json:"exchange_id"`
}

// ExchangeQuoteResponse is the library's quote summary plus the region
// the quote was priced in, which callers must send back on execute. The embedded
// pointer promotes the summary's fields to the JSON top level.
type ExchangeQuoteResponse struct {
	*exchange.ExchangeQuoteSummary
	Region string `json:"Region"`
}
