package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ricost"
)

// azureExchangeClient is the narrow interface that listExchangeableAzureRIs,
// getAzureCompatibleOfferings, and executeAzureExchange need from the Azure
// compute client. Satisfied by the compute client newAzureExchangeClient
// (provider_wiring.go) builds; a stub can be injected via
// Handler.azureExchangeFactory for tests.
type azureExchangeClient interface {
	ListExchangeableReservations(ctx context.Context) ([]azureExchangeableReservation, error)

	// CalculateExchange prices a proposed exchange without committing it.
	// Both getAzureCompatibleOfferings (read-only quote) and
	// executeAzureExchange (server-side re-quote before commit) call this;
	// only executeAzureExchange ever calls ExecuteExchange, and only with
	// the SessionID this same call just returned -- see executeAzureExchange's
	// doc comment for why a client-supplied session is never trusted.
	CalculateExchange(ctx context.Context, sources []azureExchangeableReservation, targets []azureExchangeTarget) (*azureExchangePreview, []azureCompatibleOffering, error)
	ExecuteExchange(ctx context.Context, sessionID string) (*azureExchangeResult, error)
}

// buildAzureExchangeClient returns the injected factory result when one has
// been set (test path), or constructs a real Azure compute client by resolving
// per-subscription credentials via the project's credential resolver (production
// path).
//
// Credential resolution mirrors every other Azure call in the project
// (scheduler.collectAzureForAccount, purchase/execution.go resolveAzureProvider):
// look up the registered CloudAccount whose ExternalID matches subscriptionID,
// then call credentials.ResolveAzureTokenCredentialWithOpts with the Handler's
// wired OIDC signer so managed_identity / client_secret / WIF all work.
//
// Graceful empty-state rules (returns nil client, nil error):
//   - subscriptionID is empty: GetCloudAccountByExternalID looks up an exact
//     match on external_id, and no registered account has an empty
//     external_id, so this unconditionally misses and the caller returns an
//     empty reservations list.
//   - subscriptionID is provided but no matching CloudAccount is found:
//     treated as "Azure not configured for this subscription".
//
// A genuine configuration error (missing credentials, auth failure) returns
// a non-nil error that the handler maps to a 500 with a clear message.
func (h *Handler) buildAzureExchangeClient(ctx context.Context, subscriptionID string) (azureExchangeClient, error) {
	if h.azureExchangeFactory != nil {
		return h.azureExchangeFactory(subscriptionID), nil
	}

	// Look up the registered Azure CloudAccount by its subscription ID.
	// For Azure, ExternalID stores the subscription ID (see handler_accounts_self.go:
	// buildSelfAccountRequest sets ExternalID = si.ExternalID() = si.SubscriptionID).
	account, err := h.config.GetCloudAccountByExternalID(ctx, "azure", subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("azure: look up account for subscription %q: %w", subscriptionID, err)
	}
	return h.azureExchangeClientForAccount(ctx, subscriptionID, account)
}

// azureExchangeClientForAccount is buildAzureExchangeClient for a caller that
// has already resolved the CloudAccount registered for subscriptionID (nil
// when none is), so the same row is not fetched again (issue #311). The
// injected factory still wins, and a nil account still yields the graceful
// (nil, nil) empty state.
func (h *Handler) azureExchangeClientForAccount(ctx context.Context, subscriptionID string, account *config.CloudAccount) (azureExchangeClient, error) {
	if h.azureExchangeFactory != nil {
		return h.azureExchangeFactory(subscriptionID), nil
	}
	if account == nil {
		// No Azure account registered for this subscription.
		// Return nil client so the caller emits a graceful empty state.
		return nil, nil
	}

	cred, err := credentials.ResolveAzureTokenCredentialWithOpts(ctx, account, h.credStore, credentials.AzureResolveOptions{
		Signer:    h.signer,
		IssuerURL: h.issuerURL,
	})
	if err != nil {
		return nil, fmt.Errorf("azure: resolve credentials for subscription %q: %w", subscriptionID, err)
	}

	return newAzureExchangeClient(cred, subscriptionID), nil
}

// listExchangeableAzureRIs returns active Azure VM reservations that are
// eligible for the cross-SKU/cross-region exchange flow (InstanceFlexibility
// == On, ProvisioningState == Succeeded). Requires "view:purchases" permission.
//
// ListExchangeableReservations is tenant-wide by design (see its doc
// comment: "the Azure Capacity exchange API operates on reservation order
// IDs which span subscriptions"), and that breadth does not change just
// because ?subscription_id= was supplied -- the parameter only picks which
// registered CloudAccount's credentials authenticate the call, not which
// rows come back. Left unfiltered, every caller with "view:purchases" saw
// every reservation the deployment's Azure credential could read, including
// subscriptions they are not scoped to (issue #1656). Both POST siblings on
// this resource deliberately withhold that same breadth (requireAzureSubscriptionScope
// + requireAzureSourceOwnership, both indistinguishable-denial), so this GET
// narrows the same way:
//
//   - ?subscription_id= supplied: requireAzureSubscriptionScope gates
//     whether the session may use that subscription at all (errNotFound,
//     not 403, matching the POST siblings so a scoped caller cannot probe
//     which subscriptions exist). The listing is then narrowed to that
//     subscription's own rows via BillingScopeID -- the same discriminator
//     requireAzureSourceOwnership uses -- since a reservation billed
//     elsewhere could never be named as a valid exchange source against
//     this subscription anyway.
//   - No subscription_id: the listing is narrowed to whatever the session's
//     allowed_accounts scope covers (filterAzureReservationsByScope).
//     Unrestricted/admin sessions see everything, matching every other
//     listing endpoint in this package. In production this branch is
//     defense-in-depth rather than the live path today: buildAzureExchangeClient
//     resolves subscriptionID via an exact external_id match, so an empty
//     subscriptionID never matches a registered account and the graceful
//     empty-state branch below returns before ListExchangeableReservations
//     is ever called. It becomes the live path the moment a caller can reach
//     ListExchangeableReservations without a subscription_id -- the
//     azureExchangeFactory test seam already exercises exactly that -- which
//     is why the filter stays rather than being deleted as unreachable.
//
// Rows that cannot be attributed to a registered CloudAccount (no
// BillingScopeID, or one that matches no CloudAccount) are dropped rather
// than kept, so an unresolvable scope never degrades to "show everything".
// That also keeps the two empty-result cases indistinguishable: a scoped
// caller whose own subscriptions hold zero exchangeable reservations sees
// the same empty list as one whose rows could not be attributed.
//
// When no Azure account is configured for the requested subscription (or no
// subscription is specified and none are registered), the handler returns an
// empty reservations list rather than a 500.
func (h *Handler) listExchangeableAzureRIs(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	subscriptionID := req.QueryStringParameters["subscription_id"]

	if subscriptionID != "" {
		if scopeErr := h.requireAzureSubscriptionScope(ctx, session, subscriptionID); scopeErr != nil {
			return nil, scopeErr
		}
	}

	client, err := h.buildAzureExchangeClient(ctx, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to build Azure exchange client: %w", err)
	}
	if client == nil {
		// No Azure account configured for this subscription (or no Azure accounts
		// registered at all). Return an empty list rather than a 500 so the page
		// renders a "no reservations" state instead of an opaque error banner.
		return &ExchangeableAzureRIsResponse{Reservations: []azureExchangeableReservation{}}, nil
	}

	reservations, err := client.ListExchangeableReservations(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list exchangeable Azure reservations: %w", err)
	}

	if subscriptionID != "" {
		reservations = filterAzureReservationsBySubscription(reservations, subscriptionID)
	} else {
		reservations, err = h.filterAzureReservationsByScope(ctx, session, reservations)
		if err != nil {
			return nil, err
		}
	}

	return &ExchangeableAzureRIsResponse{Reservations: reservations}, nil
}

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

// maxAzureExchangeItems caps the number of sources/targets accepted per
// Azure exchange request, guarding against an oversized request fanning out
// into an enormous CalculateExchange payload.
const maxAzureExchangeItems = 50

// AzureExchangeSourceBody is one source reservation entry in an Azure
// compatible-offerings or execute request body.
type AzureExchangeSourceBody struct {
	ReservationID string `json:"reservation_id"`
	Quantity      int32  `json:"quantity"`
}

// AzureExchangeTargetBody is one target entry. Term is the ISO 8601
// reservation term string ("P1Y", "P3Y", ...); azureReservationTermFromString
// validates it against the SDK's typed enum rather than accepting anything
// the caller sends.
type AzureExchangeTargetBody struct {
	SKU      string `json:"sku"`
	Location string `json:"location"`
	Term     string `json:"term"`
	Quantity int32  `json:"quantity"`

	// BillingScopeID is optional and is NOT the scope that gets charged:
	// the handler always derives that from the request's authorized
	// subscription_id (azureBillingScopeID), matching every other Azure
	// reservation purchase path in this repo. When supplied it must match
	// the derived scope, so a caller cannot direct the charge at a
	// different subscription than the one their permission constraints
	// were evaluated against.
	BillingScopeID string `json:"billing_scope_id,omitempty"`
}

// AzureCompatibleOfferingsRequestBody is the request body for the
// compatible-offerings endpoint.
type AzureCompatibleOfferingsRequestBody struct {
	SubscriptionID string                    `json:"subscription_id"`
	Sources        []AzureExchangeSourceBody `json:"sources"`
	Targets        []AzureExchangeTargetBody `json:"targets"`
}

// AzureCompatibleOfferingsResponse is the response for the
// compatible-offerings endpoint: the priced candidate offerings plus the
// preview (including the SessionID a subsequent execute call would need,
// though execute never trusts a client-supplied session -- see
// executeAzureExchange).
type AzureCompatibleOfferingsResponse struct {
	Offerings []azureCompatibleOffering `json:"offerings"`
	Preview   *azureExchangePreview     `json:"preview"`
}

// AzureExecuteExchangeRequestBody is the request body for the execute
// endpoint. MaxPaymentDue + Currency are mandatory safety guardrails: the
// handler refuses to execute an exchange whose fresh quote exceeds the cap
// or is denominated in a different currency.
type AzureExecuteExchangeRequestBody struct {
	SubscriptionID string                    `json:"subscription_id"`
	Sources        []AzureExchangeSourceBody `json:"sources"`
	Targets        []AzureExchangeTargetBody `json:"targets"`
	MaxPaymentDue  string                    `json:"max_payment_due"`
	Currency       string                    `json:"currency"`
}

// AzureExecuteExchangeResponse is the response from a successfully executed
// Azure exchange.
type AzureExecuteExchangeResponse struct {
	SessionID          string   `json:"session_id"`
	Status             string   `json:"status"`
	NetPayable         *float64 `json:"net_payable"`
	NetPayableCurrency string   `json:"net_payable_currency,omitempty"`
	RefundsTotal       *float64 `json:"refunds_total"`
	PurchasesTotal     *float64 `json:"purchases_total"`
}

// validateAzureExchangeSources checks the shared sources[] shape for both
// the offerings and execute request bodies.
func validateAzureExchangeSources(sources []AzureExchangeSourceBody) error {
	if len(sources) == 0 {
		return NewClientError(400, "sources is required")
	}
	if len(sources) > maxAzureExchangeItems {
		return NewClientError(400, fmt.Sprintf("sources exceeds the maximum of %d items", maxAzureExchangeItems))
	}
	for i, s := range sources {
		if s.ReservationID == "" {
			return NewClientError(400, fmt.Sprintf("sources[%d].reservation_id is required", i))
		}
		if s.Quantity < 1 {
			return NewClientError(400, fmt.Sprintf("sources[%d].quantity must be >= 1", i))
		}
	}
	return nil
}

// azureBillingScopeID returns the ARM billing scope that a purchase against
// subscriptionID is charged to.
//
// The billing scope is always derived from the request's subscription_id,
// never accepted from the caller. Every other Azure reservation purchase
// path in this repo does the same (ComputeClient.buildReservationBody and
// the database / cache / search / cosmosdb / synapse / managedredis
// clients all build "/subscriptions/{their own subscriptionID}"). It also
// keeps the charge inside the scope authorization actually checked: the
// execute:ri-exchange AccountIDs constraint is evaluated against the
// CloudAccount registered for subscription_id, so letting a caller name a
// different billing scope would move the money outside the account whose
// constraints were verified.
func azureBillingScopeID(subscriptionID string) string {
	return "/subscriptions/" + subscriptionID
}

// validateAzureExchangeTargets checks the shared targets[] shape for both
// the offerings and execute request bodies. subscriptionID is the already-
// validated request subscription; a target may omit billing_scope_id
// entirely, but may not name a scope other than that subscription's.
func validateAzureExchangeTargets(targets []AzureExchangeTargetBody, subscriptionID string) error {
	if len(targets) == 0 {
		return NewClientError(400, "targets is required")
	}
	if len(targets) > maxAzureExchangeItems {
		return NewClientError(400, fmt.Sprintf("targets exceeds the maximum of %d items", maxAzureExchangeItems))
	}
	scope := azureBillingScopeID(subscriptionID)
	for i, t := range targets {
		// Trimmed, like location below: the #1642 idempotency fingerprint
		// normalizes surrounding whitespace away, so a blank-but-not-empty
		// SKU would reach it as "" and collapse onto other blank spellings.
		if strings.TrimSpace(t.SKU) == "" {
			return NewClientError(400, fmt.Sprintf("targets[%d].sku is required", i))
		}
		// Blank-but-not-empty is rejected too: targetLocations trims before
		// it builds the Regions constraint, so a whitespace-only location
		// would otherwise reach the permission check as "" -- a region no
		// permission can name.
		if strings.TrimSpace(t.Location) == "" {
			return NewClientError(400, fmt.Sprintf("targets[%d].location is required", i))
		}
		if t.BillingScopeID != "" && !strings.EqualFold(t.BillingScopeID, scope) {
			return NewClientError(400, fmt.Sprintf(
				"targets[%d].billing_scope_id %q is not the billing scope of subscription %q; omit it to charge the subscription's own scope",
				i, t.BillingScopeID, subscriptionID))
		}
		if t.Quantity < 1 {
			return NewClientError(400, fmt.Sprintf("targets[%d].quantity must be >= 1", i))
		}
		if _, err := azureReservationTermFromString(t.Term); err != nil {
			return NewClientError(400, fmt.Sprintf("targets[%d].term: %v", i, err))
		}
	}
	return nil
}

// validateAzureOfferingsBody validates the compatible-offerings request
// body. Extracted so getAzureCompatibleOfferings and
// validateAzureExecuteBody share the same check without exceeding the
// gocyclo threshold (mirrors validateExecuteExchangeBody's precedent for
// the AWS execute handler).
func validateAzureOfferingsBody(body AzureCompatibleOfferingsRequestBody) error {
	if body.SubscriptionID == "" {
		return NewClientError(400, "subscription_id is required")
	}
	if err := validateAzureExchangeSources(body.Sources); err != nil {
		return err
	}
	return validateAzureExchangeTargets(body.Targets, body.SubscriptionID)
}

// validateAzureExecuteBody validates the execute request body: the shared
// offerings validation plus the mandatory spend-cap and currency guardrails.
func validateAzureExecuteBody(body AzureExecuteExchangeRequestBody) error {
	if err := validateAzureOfferingsBody(AzureCompatibleOfferingsRequestBody{
		SubscriptionID: body.SubscriptionID,
		Sources:        body.Sources,
		Targets:        body.Targets,
	}); err != nil {
		return err
	}
	if body.MaxPaymentDue == "" {
		return NewClientError(400, "max_payment_due is required as a safety guardrail")
	}
	if body.Currency == "" {
		return NewClientError(400, "currency is required")
	}
	return nil
}

// toAzureExchangeSources keeps the server-listed reservation metadata and
// applies the requested quantities after requireAzureSourceOwnership succeeds.
func toAzureExchangeSources(sources []AzureExchangeSourceBody, owned []azureExchangeableReservation) []azureExchangeableReservation {
	byID := make(map[string]azureExchangeableReservation, len(owned))
	for i := range owned {
		byID[strings.ToLower(owned[i].ReservationID)] = owned[i]
	}
	out := make([]azureExchangeableReservation, len(sources))
	for i, s := range sources {
		out[i] = byID[strings.ToLower(s.ReservationID)]
		out[i].Quantity = s.Quantity
	}
	return out
}

// toAzureExchangeTargets converts the HTTP-shaped targets into the
// provider-layer shape, re-parsing the term string and deriving each
// target's billing scope from subscriptionID rather than from the request
// body (see azureBillingScopeID). validateAzureExchangeTargets must be
// called first; a term error here indicates an internal invariant break
// rather than a fresh client mistake.
func toAzureExchangeTargets(targets []AzureExchangeTargetBody, subscriptionID string) ([]azureExchangeTarget, error) {
	out := make([]azureExchangeTarget, len(targets))
	scope := azureBillingScopeID(subscriptionID)
	for i, t := range targets {
		term, err := azureReservationTermFromString(t.Term)
		if err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		out[i] = azureExchangeTarget{
			SKU:            t.SKU,
			Location:       t.Location,
			Term:           term,
			Quantity:       t.Quantity,
			BillingScopeID: scope,
		}
	}
	return out, nil
}

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

// getAzureCompatibleOfferings prices a proposed Azure RI exchange and
// returns the compatible offerings Azure is willing to accept plus the cost
// preview, without committing anything. Requires "view:purchases" permission
// plus allowed_accounts scope over the requested subscription, mirroring the
// AWS quote endpoint.
//
// POST /api/ri-exchange/azure-instances/compatible-offerings.
func (h *Handler) getAzureCompatibleOfferings(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	var body AzureCompatibleOfferingsRequestBody
	if err = json.Unmarshal([]byte(req.Body), &body); err != nil {
		return nil, NewClientError(400, "invalid request body")
	}
	if validateErr := validateAzureOfferingsBody(body); validateErr != nil {
		return nil, validateErr
	}
	if scopeErr := h.requireAzureSubscriptionScope(ctx, session, body.SubscriptionID); scopeErr != nil {
		return nil, scopeErr
	}

	client, err := h.buildAzureExchangeClient(ctx, body.SubscriptionID)
	if err != nil {
		return nil, fmt.Errorf("failed to build Azure exchange client: %w", err)
	}
	if client == nil {
		return nil, NewClientError(404, fmt.Sprintf("no Azure account registered for subscription %q", body.SubscriptionID))
	}

	sources, err := checkAzureSourceOwnership(ctx, client, body.Sources, body.SubscriptionID)
	if err != nil {
		return nil, err
	}

	targets, err := toAzureExchangeTargets(body.Targets, body.SubscriptionID)
	if err != nil {
		return nil, err
	}

	preview, offerings, err := client.CalculateExchange(ctx, sources, targets)
	if err != nil {
		logging.Errorf("azure compatible offerings failed: %v", err)
		return nil, mapAzureExchangeError("failed to find compatible offerings", err)
	}

	return &AzureCompatibleOfferingsResponse{Offerings: offerings, Preview: preview}, nil
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

// listRIsAccount resolves the AWS account the convertible RIs are read from
// (the deployment's ambient credentials, via STS) and applies the ?account_id=
// chip filter (issue #871). With a filter, an STS failure fails closed and a
// different account is out of scope. Without one the account only labels the
// rows (#1692), so a failure is logged and the label is left empty.
func (h *Handler) listRIsAccount(ctx context.Context, requested string) (accountID string, inScope bool, err error) {
	resolve := h.resolveAWSAccountID
	if h.riInstancesAccountResolver != nil {
		resolve = h.riInstancesAccountResolver
	}
	running, resolveErr := resolve(ctx)
	if requested == "" {
		if resolveErr != nil {
			logging.Warnf("listConvertibleRIs: could not resolve the running AWS account: %v", resolveErr)
			return "", true, nil
		}
		return running, true, nil
	}
	if resolveErr != nil {
		return "", false, fmt.Errorf("failed to resolve running AWS account for RI scope: %w", resolveErr)
	}
	return running, running == requested, nil
}

// listConvertibleRIs returns all active convertible Reserved Instances for
// the running AWS account.
//
// The optional ?account_id= query parameter narrows the listing to a single
// AWS account so the page honors the Main Header global account filter
// (issue #871). Convertible RIs are read from the deployment's ambient AWS
// credentials, which resolve to exactly one account number; when the chip
// selects a different account, none of these RIs belong to it, so we return
// an empty list rather than the unscoped fleet. A real STS failure fails
// closed (returns an error) instead of silently leaking the ambient account's
// RIs under another account's filter.
func (h *Handler) listConvertibleRIs(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope: a restricted user must
	// only see RIs for the deployment's registered AWS account.
	if inScope, sErr := h.reshapeCloudAccountInScope(ctx, session); sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &ConvertibleRIsResponse{Instances: []convertibleRIWithAccount{}}, nil
	}

	runningAccountID, inScope, err := h.listRIsAccount(ctx, req.QueryStringParameters["account_id"])
	if err != nil {
		return nil, err
	} else if !inScope {
		return &ConvertibleRIsResponse{Instances: []convertibleRIWithAccount{}}, nil
	}

	region := req.QueryStringParameters["region"]
	cfg, err := h.loadAWSConfigWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := h.buildReshapeEC2Client(cfg)
	instances, err := ec2Client.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}

	out := make([]convertibleRIWithAccount, len(instances))
	for i := range instances {
		out[i] = convertibleRIWithAccount{ec2ConvertibleRI: instances[i], AccountID: runningAccountID}
	}
	return &ConvertibleRIsResponse{Instances: out}, nil
}

// getRIUtilization returns per-RI utilization from Cost Explorer.
func (h *Handler) getRIUtilization(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Validate input before resolving the account, so a malformed request
	// makes no STS or account lookup.
	lookbackDays, err := parseLookbackDaysParam(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope. The resolved account also
	// keys the utilization cache, so two accounts never share a cached row.
	cloudAccountID, inScope, sErr := h.resolveReshapeAccountScope(ctx, session)
	if sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &RIUtilizationResponse{Utilization: []riUtilization{}}, nil
	}

	region := req.QueryStringParameters["region"]
	cfg, err := h.loadAWSConfigWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	// Adopt the resolved region when the caller omits ?region= so the CE
	// utilization filter and the cache key both scope to the region the
	// AWS client is actually talking to (mirrors getReshapeRecommendations).
	if region == "" {
		region = cfg.Region
	}

	recsAdapter := h.buildReshapeRecsClient(cfg)
	fetch := func(fetchCtx context.Context, days int) ([]riUtilization, error) {
		return recsAdapter.GetRIUtilization(fetchCtx, days, region)
	}
	cacheKey := config.RIUtilizationCacheKey{CloudAccountID: cloudAccountID, Region: region, LookbackDays: lookbackDays}
	utilization, err := h.getRIUtilizationCache().getOrFetch(ctx, cacheKey, riUtilizationCacheTTL, riUtilizationCacheStaleTTL, fetch)
	if err != nil {
		return nil, fmt.Errorf("failed to get RI utilization: %w", err)
	}

	utilization, err = h.restrictUtilizationToDeploymentRIs(ctx, session, cfg, utilization)
	if err != nil {
		return nil, err
	}

	return &RIUtilizationResponse{Utilization: utilization}, nil
}

// restrictUtilizationToDeploymentRIs drops utilization rows that do not belong
// to the deployment account for a scoped session. Cost Explorer
// GetReservationUtilization, called from an AWS payer (management) account,
// returns every linked member account's RIs and the rows carry only the RI id,
// so the rows cannot be matched to a member account. The deployment account's
// own RIs (EC2 DescribeReservedInstances never lists members' RIs) are the
// only rows the scope gate in the caller has authorized; unknown ids are
// dropped, so an unregistered or out-of-scope member's RIs stay hidden.
// Unrestricted sessions keep every row and make no extra AWS call.
func (h *Handler) restrictUtilizationToDeploymentRIs(ctx context.Context, session *Session, cfg aws.Config, utilization []riUtilization) ([]riUtilization, error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return utilization, nil
	}
	own, err := h.buildReshapeEC2Client(cfg).ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}
	ownIDs := make(map[string]struct{}, len(own))
	for i := range own {
		ownIDs[own[i].ReservedInstanceID] = struct{}{}
	}
	visible := make([]riUtilization, 0, len(utilization))
	for i := range utilization {
		if _, ok := ownIDs[utilization[i].ReservedInstanceID]; ok {
			visible = append(visible, utilization[i])
		}
	}
	return visible, nil
}

// parseLookbackDaysParam parses and validates the "lookback_days" query parameter.
// Returns 30 as default when the parameter is absent.
func parseLookbackDaysParam(params map[string]string) (int, error) {
	days := params["lookback_days"]
	if days == "" {
		return 30, nil
	}
	d, err := strconv.Atoi(days)
	if err != nil || d < 1 || d > 365 {
		return 0, NewClientError(400, "lookback_days must be between 1 and 365")
	}
	return d, nil
}

// parseThresholdParam parses and validates the "threshold" query parameter.
func parseThresholdParam(params map[string]string) (float64, error) {
	t := params["threshold"]
	if t == "" {
		return 95.0, nil
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100 {
		return 0, NewClientError(400, "threshold must be a number between 0 and 100")
	}
	return f, nil
}

// monthlyCostFromConvertibleRI delegates to ricost.MonthlyCostFromConvertibleRI,
// the single implementation of the convertible RI monthly cost formula shared
// with internal/server. Its error wraps ricost.ErrNonPositiveDuration or
// ricost.ErrNoPricing, and callers must exclude the RI.
func monthlyCostFromConvertibleRI(ri ec2ConvertibleRI) (float64, error) {
	return ricost.MonthlyCostFromConvertibleRI(ri)
}

// excludedRIReason is the user-facing reason for an RI left out of reshape
// analysis because monthlyCostFromConvertibleRI returned err.
func excludedRIReason(err error) string {
	if errors.Is(err, ricost.ErrNoPricing) {
		return "AWS returned no pricing (fixed, usage and recurring charges total zero or less), so its monthly cost cannot be computed"
	}
	return "AWS returned no term length (duration), so its monthly cost cannot be computed"
}

// convertToExchangeTypes converts provider-specific types to the exchange package types.
// RIs whose monthly cost cannot be computed are returned as excluded rather
// than dropped silently, so the response can surface them.
func convertToExchangeTypes(instances []ec2ConvertibleRI, utilData []riUtilization) ([]exchange.RIInfo, []exchange.UtilizationInfo, []ExcludedRI) {
	var excluded []ExcludedRI
	riInfos := make([]exchange.RIInfo, 0, len(instances))
	for i := range instances {
		inst := instances[i]
		monthlyCost, err := monthlyCostFromConvertibleRI(inst)
		if err != nil {
			logging.Warnf("convertToExchangeTypes: excluding RI from reshape: %v", err)
			excluded = append(excluded, ExcludedRI{RIID: inst.ReservedInstanceID, Reason: excludedRIReason(err)})
			continue
		}
		riInfos = append(riInfos, exchange.RIInfo{
			ID:                  inst.ReservedInstanceID,
			InstanceType:        inst.InstanceType,
			InstanceCount:       inst.InstanceCount,
			OfferingClass:       "convertible",
			NormalizationFactor: inst.NormalizationFactor,
			MonthlyCost:         monthlyCost,
			CurrencyCode:        inst.CurrencyCode,
			// Plumb the AWS-reported RI duration straight through —
			// reshape's term-match guard rejects alternatives whose
			// TermSeconds differs from the source so a 3y RI never
			// surfaces as an alternative to a 1y commitment.
			TermSeconds: inst.Duration,
		})
	}

	utilInfos := make([]exchange.UtilizationInfo, len(utilData))
	for i, u := range utilData {
		utilInfos[i] = exchange.UtilizationInfo{
			RIID:               u.ReservedInstanceID,
			UtilizationPercent: u.UtilizationPercent,
		}
	}

	return riInfos, utilInfos, excluded
}

// reshapeRequestParams groups parsed query parameters for getReshapeRecommendations.
type reshapeRequestParams struct {
	region       string
	threshold    float64
	lookbackDays int
}

// parseReshapeParams parses the threshold, lookback_days, and region query
// parameters for getReshapeRecommendations in a single call, reducing the
// number of error-check branches in the handler to keep it within the
// gocyclo limit.
func parseReshapeParams(params map[string]string) (reshapeRequestParams, error) {
	threshold, err := parseThresholdParam(params)
	if err != nil {
		return reshapeRequestParams{}, err
	}
	lookbackDays, err := parseLookbackDaysParam(params)
	if err != nil {
		return reshapeRequestParams{}, err
	}
	return reshapeRequestParams{
		threshold:    threshold,
		lookbackDays: lookbackDays,
		region:       params["region"],
	}, nil
}

// getReshapeRecommendations orchestrates fetching convertible RIs + utilization
// and returns reshape recommendations.
func (h *Handler) getReshapeRecommendations(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Validate input before resolving the account, so a malformed request
	// makes no STS or account lookup.
	p, err := parseReshapeParams(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope. The resolved account also
	// keys the utilization cache and scopes the recs lookup below.
	cloudAccountID, inScope, sErr := h.resolveReshapeAccountScope(ctx, session)
	if sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &ReshapeRecommendationsResponse{Recommendations: []exchange.ReshapeRecommendation{}}, nil
	}

	cfg, err := h.loadAWSConfigWithRegion(ctx, p.region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	// Normalize region: when the caller omits ?region=, loadAWSConfigWithRegion
	// resolves a default from the AWS SDK chain but the local string stays
	// empty — which would scope the RI utilization cache and the recs lookup
	// unscoped, leaking alternatives from other regions onto the reshape page.
	// Adopt the resolved region so every downstream consumer sees the same
	// value the AWS clients are actually talking to.
	if p.region == "" {
		p.region = cfg.Region
	}

	ec2Client := h.buildReshapeEC2Client(cfg)
	instances, err := ec2Client.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}

	recsAdapter := h.buildReshapeRecsClient(cfg)
	fetch := func(fetchCtx context.Context, days int) ([]riUtilization, error) {
		return recsAdapter.GetRIUtilization(fetchCtx, days, p.region)
	}
	cacheKey := config.RIUtilizationCacheKey{CloudAccountID: cloudAccountID, Region: p.region, LookbackDays: p.lookbackDays}
	utilData, err := h.getRIUtilizationCache().getOrFetch(ctx, cacheKey, riUtilizationCacheTTL, riUtilizationCacheStaleTTL, fetch)
	if err != nil {
		return nil, fmt.Errorf("failed to get RI utilization: %w", err)
	}

	riInfos, utilInfos, excluded := convertToExchangeTypes(instances, utilData)
	// Cross-family alternatives are sourced from the cached AWS Cost
	// Explorer purchase recommendations table in Postgres — no per-rec
	// DescribeReservedInstancesOfferings fan-out, no hand-curated
	// peer-family allowlist. The lookup is scoped to the running AWS
	// account (when registered) so a multi-tenant deployment can't
	// surface another tenant's recs. Empty resolved account ID means
	// "no scope filter" for ambient-credentials deployments where
	// CloudAccount registration hasn't happened yet; a real ListCloudAccounts
	// error aborted the request at the scope check above instead of silently
	// falling through to an unscoped query that could match the wrong
	// tenant's recs.
	currencyCode := firstNonEmptyCurrency(instances)
	lookup := purchaseRecLookupFromStore(h.config, cloudAccountID)
	offered := OfferedInstanceTypes(ctx, p.region, ec2Client.GetValidResourceTypes)
	recs := exchange.AnalyzeReshapingWithRecs(ctx, riInfos, utilInfos, p.threshold, p.region, currencyCode, lookup,
		exchange.WithOfferedInstanceTypes(offered))

	resp := &ReshapeRecommendationsResponse{Recommendations: recs, ExcludedRIs: excluded}
	h.attachReshapeStaleness(ctx, resp)
	return resp, nil
}

// attachReshapeStaleness populates the RecsStaleness and RecsCollectedAt
// fields on resp from the recommendations_state table. Non-fatal: errors
// are logged and the response ships without staleness metadata so the
// reshape table itself is unaffected by a DB read-side failure.
func (h *Handler) attachReshapeStaleness(ctx context.Context, resp *ReshapeRecommendationsResponse) {
	freshness, err := h.config.GetRecommendationsFreshness(ctx)
	if err != nil {
		logging.Warnf("getReshapeRecommendations: could not check recs freshness (banner suppressed): %v", err)
		return
	}
	resp.RecsCollectedAt = freshness.LastCollectedAt
	if freshness.LastCollectedAt == nil {
		// Cold start: cache was never populated — treat as hard-stale so the
		// banner fires on a fresh deployment rather than silently hiding it.
		resp.RecsStaleness = "hard"
	} else {
		resp.RecsStaleness = classifyRecsAge(time.Since(*freshness.LastCollectedAt))
	}
}

// firstNonEmptyCurrency returns the first RI CurrencyCode set, or "" when none
// is, so the response omits currency_code instead of claiming USD (#361).
func firstNonEmptyCurrency(instances []ec2ConvertibleRI) string {
	for i := range instances {
		inst := instances[i]
		if inst.CurrencyCode != "" {
			return inst.CurrencyCode
		}
	}
	return ""
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

// ConvertibleRIsResponse holds the list of convertible RIs.
type ConvertibleRIsResponse struct {
	Instances []convertibleRIWithAccount `json:"instances"`
}

// convertibleRIWithAccount names the AWS account an RI belongs to, so the UI
// can show it before an exchange (#1692). AccountID is empty when the running
// account could not be resolved.
type convertibleRIWithAccount struct {
	ec2ConvertibleRI
	AccountID string `json:"account_id,omitempty"`
}

// ExchangeableAzureRIsResponse holds the list of Azure VM reservations that
// are eligible for the cross-SKU/cross-region exchange flow.
type ExchangeableAzureRIsResponse struct {
	Reservations []azureExchangeableReservation `json:"reservations"`
}

// RIUtilizationResponse holds per-RI utilization data.
type RIUtilizationResponse struct {
	Utilization []riUtilization `json:"utilization"`
}

// ReshapeRecommendationsResponse holds reshape recommendations.
//
// RecsStaleness is empty when the underlying Cost Explorer cache is
// fresh, "soft" when it is older than reshapeSoftStaleThreshold (12 h),
// and "hard" when it is older than reshapeHardStaleThreshold (24 h).
// RecsCollectedAt carries the raw timestamp so the frontend can build
// its own relative-time label ("last collected 23h ago").
type ReshapeRecommendationsResponse struct {
	RecsCollectedAt *time.Time                       `json:"recs_collected_at,omitempty"`
	RecsStaleness   string                           `json:"recs_staleness,omitempty"`
	Recommendations []exchange.ReshapeRecommendation `json:"recommendations"`
	// ExcludedRIs lists RIs left out of cost-based analysis (and so have no
	// recommendation) because their monthly cost could not be computed.
	ExcludedRIs []ExcludedRI `json:"excluded_ris,omitempty"`
}

// ExcludedRI names a convertible RI omitted from reshape analysis and why.
type ExcludedRI struct {
	RIID   string `json:"ri_id"`
	Reason string `json:"reason"`
}

// reshapeSoftStaleThreshold is the age at which the reshape recs banner
// transitions to "soft" warning: data may be up to 12 h old.
const reshapeSoftStaleThreshold = 12 * time.Hour

// reshapeHardStaleThreshold is the age at which the reshape recs banner
// transitions to "hard" warning: data is more than 24 h old.
const reshapeHardStaleThreshold = 24 * time.Hour

// classifyRecsAge maps a data age to the staleness label surfaced in
// ReshapeRecommendationsResponse.RecsStaleness. The zero duration
// (cold-cache path: no LastCollectedAt) is treated as "hard" so the
// banner fires on a fresh deployment rather than silently hiding it.
func classifyRecsAge(age time.Duration) string {
	switch {
	case age >= reshapeHardStaleThreshold:
		return "hard"
	case age >= reshapeSoftStaleThreshold:
		return "soft"
	default:
		return ""
	}
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

// exchangeAccountIDsInScope returns, sorted, every STS account id whose
// exchange records the scope covers under exchangeRecordInScope. A covered id
// is either a registered account's external id or a literal allow-list entry,
// so testing those candidates finds them all. Literal entries that are
// not STS ids (a UUID or a name) are kept too: they match no stored record,
// and keeping them makes the set exactly what exchangeRecordInScope accepts.
func exchangeAccountIDsInScope(allowed auth.AccountScope, accounts []config.CloudAccount) []string {
	candidates := make(map[string]struct{}, len(allowed.Accounts)+len(accounts))
	for _, a := range allowed.Accounts {
		candidates[a] = struct{}{}
	}
	for i := range accounts {
		candidates[accounts[i].ExternalID] = struct{}{}
	}
	ids := make([]string, 0, len(candidates))
	for id := range candidates {
		if exchangeRecordInScope(allowed, accounts, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// exchangeRecordInScope reports whether the scope covers an exchange record's
// STS account id, directly or through the single registered AWS account that
// owns it.
func exchangeRecordInScope(allowed auth.AccountScope, accounts []config.CloudAccount, stsAccountID string) bool {
	acct := uniqueAccountByExternalID(accounts, "aws", stsAccountID)
	if acct == nil {
		return allowed.Allows(stsAccountID, "")
	}
	return allowed.Allows(stsAccountID, acct.Name) || allowed.Allows(acct.ID, acct.Name)
}

// approveRIExchange handles approval of a pending RI exchange.
//
// Three-mode dispatch mirroring approvePurchase (issue #286, issue #300):
//
//  1. Session present AND RBAC-authorized (admin / approve-any / approve-own
//     match) -> session-authed approve, regardless of whether a token is also
//     in the URL. Closes issue #300.
//  2. token != "" -> legacy email-link flow. validateExchangeApproval enforces
//     the token-equality check; the permission-denied fall-through ensures a
//     logged-in user without approve-* can still use an email link they hold.
//  3. token == "": a session denial is returned as-is (issue #173); with no
//     session, approveRIExchangeViaSession's CSRF check fails first and the
//     request gets 403 (never 401).
func (h *Handler) approveRIExchange(ctx context.Context, req *events.LambdaFunctionURLRequest, id, token string) (any, error) {
	if session := h.tryGetSession(ctx, req); session != nil {
		// Quick RBAC pre-check (no record fetch needed): does this session hold
		// ANY approve right? If yes, hand off to approveRIExchangeViaSession
		// which will re-check ownership with the actual record. If 403, fall
		// through to the token branch so email-link holders can still approve.
		switch err := h.sessionHasApproveRight(ctx, session); {
		case err == nil:
			result, sessErr := h.approveRIExchangeViaSession(ctx, req, id, session)
			if sessErr == nil {
				return result, nil
			}
			// Record-level RBAC denied (e.g. approve-own user is not the creator).
			// If a token is present, preserve legacy token flow; otherwise surface the error.
			//
			// A CSRF rejection must NOT fall through, even with a token present.
			// fallsThroughToToken matches any 403, so without this it cannot tell a
			// forged cross-site request from a legitimate approve-own denial, and the
			// request would proceed on the token alone: the exchange executes, but as
			// an unattributed system approval (transitioned_by NULL, no approved_by
			// stamp) on a money path. Checked first so the sentinel is never reached
			// by the fall-through test below.
			if errors.Is(sessErr, errCSRFRejected) || !fallsThroughToToken(sessErr, token) {
				return nil, sessErr
			}
		case token != "" && isPermissionDenied(err):
			// Logged-in user without approve-* may still hold a valid email token.
		default:
			return nil, err
		}
	}

	if token != "" {
		return h.approveRIExchangeViaToken(ctx, req, id, token)
	}

	return h.approveRIExchangeViaSession(ctx, req, id, nil)
}

// approveRIExchangeViaToken is the legacy email-link branch of approveRIExchange.
// It validates the approval token, transitions the exchange to processing, and
// executes it. Extracted to keep approveRIExchange within cyclomatic-complexity limits.
func (h *Handler) approveRIExchangeViaToken(ctx context.Context, req *events.LambdaFunctionURLRequest, id, token string) (any, error) {
	record, err := h.validateExchangeApproval(ctx, id, token)
	if err != nil {
		return nil, err
	}
	// The deep link forces a login, so the approver's session is resolvable here.
	// When it is not (pure email-client flow), the actor stays NULL.
	session := h.tryGetSession(ctx, req)
	err = h.requireDifferentRIExchangeApprover(ctx, session, record)
	if err != nil {
		return nil, err
	}

	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", "processing", resolveCreatorUserID(session))
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeApproveConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeApproveConflictMsg)
	}

	result, execErr := h.executeApprovedExchange(ctx, id, record)
	if execErr == nil && session != nil {
		h.stampRIExchangeApprover(ctx, id, session)
	}
	return result, execErr
}

// stampRIExchangeApprover records session.Email as approved_by. Best-effort: the
// exchange already executed, so a stamp failure is logged, not surfaced.
func (h *Handler) stampRIExchangeApprover(ctx context.Context, id string, session *Session) {
	// Runs after money moved, so the audit write must not die with the request (#790).
	ctx, cancel := ctxutil.DetachedWithTimeout(ctx, riExchangeTerminalSaveTimeout)
	defer cancel()
	if err := h.config.StampRIExchangeApprovedBy(ctx, id, session.Email); err != nil {
		logging.Errorf("failed to stamp approved_by on exchange %s: %v", id, err)
	}
}

// approveRIExchangeViaSession is the session-authed branch of approveRIExchange
// (issue #300). Enforces the approve-any/approve-own RBAC matrix, then atomically
// transitions pending -> processing and executes the exchange. Stamps
// session.Email onto the approved_by column as an audit trail.
//
// The session parameter may be non-nil (already validated by the caller) or nil
// (an absent session never reaches requireSession: the CSRF check above
// rejects it with 403).
func (h *Handler) approveRIExchangeViaSession(ctx context.Context, req *events.LambdaFunctionURLRequest, id string, session *Session) (any, error) {
	// This endpoint is AuthPublic so the outer middleware skips CSRF.
	// Enforce it here for the session-authed sub-path, mirroring
	// approvePurchaseViaSession / cancelPurchaseViaSession: the session
	// bearer token is cookie-equivalent and must be CSRF-protected (issue
	// #1757 -- this call was previously reachable with no CSRF token at
	// all, despite a comment claiming parity with the purchases path).
	if err := h.validateCSRF(ctx, req); err != nil {
		return nil, errCSRFRejected
	}

	var err error
	if session == nil {
		session, err = h.requireSession(ctx, req)
		if err != nil {
			return nil, err
		}
	}

	record, err := h.fetchAndAuthorizeRIExchange(ctx, session, id)
	if err != nil {
		return nil, err
	}
	err = h.requireDifferentRIExchangeApprover(ctx, session, record)
	if err != nil {
		return nil, err
	}

	// Session-authed approval: stamp the session user as the actor.
	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", "processing", resolveCreatorUserID(session))
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeApproveConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeApproveConflictMsg)
	}

	result, execErr := h.executeApprovedExchange(ctx, id, record)

	if execErr == nil {
		h.stampRIExchangeApprover(ctx, id, session)
	}

	return result, execErr
}

// fetchAndAuthorizeRIExchange looks up the exchange record by id, verifies that
// session is authorized to approve it, and then checks that it is in "pending"
// state. Extracted from approveRIExchangeViaSession to keep that function under
// the cyclomatic-complexity limit.
//
// Ownership is checked before existence and status (issue #1428): an
// approve-own session gets the same 403 for a missing id and for another
// user's exchange, pending or not, so it cannot learn which ids exist or read
// their status. The 403 also falls through to the email token like any other
// ownership refusal. Approve-any sessions and the admin key may approve any
// exchange, so they keep the 404/409 distinction.
func (h *Handler) fetchAndAuthorizeRIExchange(ctx context.Context, session *Session, id string) (*config.RIExchangeRecord, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}

	// Same deployment-account scope as executeExchange (#93), checked before
	// the fetch so an out-of-scope id is indistinguishable from a missing one.
	inScope, err := h.reshapeCloudAccountInScope(ctx, session)
	if err != nil {
		return nil, err
	}
	if !inScope {
		return nil, errNotFound
	}

	record, err := h.getRIExchangeRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := h.authorizeSessionApproveRIExchange(ctx, session, record); err != nil {
		return nil, err
	}
	if record == nil {
		return nil, NewClientError(404, "exchange record not found")
	}
	if record.Status != "pending" {
		return nil, NewClientError(409, fmt.Sprintf("exchange %s cannot be approved (status=%s)", id, record.Status))
	}

	return record, nil
}

// sessionHasApproveRight returns nil when the session holds ANY approve right
// on purchases (admin / approve-any / approve-own) without checking ownership.
// Used by the three-mode dispatch in approveRIExchange to decide whether to route
// to approveRIExchangeViaSession before fetching the record.
func (h *Handler) sessionHasApproveRight(ctx context.Context, session *Session) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the approve-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}
	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}
	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return err
	}
	if hasOwn {
		return nil
	}
	return NewClientError(403, "permission denied: requires approve-any or approve-own on purchases")
}

// authorizeSessionApproveRIExchange returns nil when the session is permitted to
// approve the given RI exchange record under the approve-any / approve-own RBAC rules
// (issue #300). Mirrors authorizeSessionApprove from handler_purchases_approve.go.
//
// The RI exchange shares ResourcePurchases because approval is conceptually
// "approving a purchase action on a different resource type" per the issue spec,
// which prefers reusing the existing verbs to keep the matrix small.
//
// record may be nil (no such exchange). An approve-own session is refused for
// it exactly as for another user's record (issue #1428); the admin key and
// approve-any pass, and the caller answers 404.
func (h *Handler) authorizeSessionApproveRIExchange(ctx context.Context, session *Session, record *config.RIExchangeRecord) error {
	// Stateless admin API key: full access, no user row. Administrators-group
	// users pass via the approve-any check below.
	if session.UserID == apiKeyAdminUserID {
		return nil
	}
	if h.auth == nil {
		return NewClientError(500, "authentication service not configured")
	}

	hasAny, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveAny)
	if err != nil {
		return err
	}
	if hasAny {
		return nil
	}

	hasOwn, err := h.sessionHoldsVerb(ctx, session, auth.ActionApproveOwn)
	if err != nil {
		return err
	}
	if !hasOwn {
		return NewClientError(403, "permission denied: requires approve-any or approve-own on purchases")
	}

	// approve-own: only allow if the session user created this exchange. A
	// missing record answers the same, so the refusal is no existence oracle.
	if record == nil || record.CreatedByUserID == nil || *record.CreatedByUserID != session.UserID {
		return NewClientError(403, "permission denied: cannot approve another user's pending exchange")
	}

	return nil
}

// rejectRIExchange handles rejection of a pending RI exchange via token.
func (h *Handler) rejectRIExchange(ctx context.Context, id, token string) (any, error) {
	if err := validateUUID(id); err != nil {
		return nil, err
	}
	if token == "" {
		return nil, NewClientError(400, "rejection token is required")
	}

	record, err := h.getRIExchangeRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	if tokenErr := checkExchangeToken(record, token, "rejection"); tokenErr != nil {
		return nil, tokenErr
	}

	// Token-based rejection: no session user, so transitioned_by = NULL.
	transitioned, err := h.config.TransitionRIExchangeStatus(ctx, id, "pending", config.StatusCanceled, nil)
	if err != nil {
		return nil, riExchangeTransitionError(err, riExchangeRejectConflictMsg)
	}
	if transitioned == nil {
		return nil, NewClientError(409, riExchangeRejectConflictMsg)
	}

	return map[string]string{"status": config.StatusCanceled}, nil
}

// RIExchangeConfigResponse is the response for GET /api/ri-exchange/config.
type RIExchangeConfigResponse struct {
	Mode                     string  `json:"mode"`
	UtilizationThreshold     float64 `json:"utilization_threshold"`
	MaxPaymentPerExchangeUSD float64 `json:"max_payment_per_exchange_usd"`
	MaxPaymentDailyUSD       float64 `json:"max_payment_daily_usd"`
	LookbackDays             int     `json:"lookback_days"`
	AutoExchangeEnabled      bool    `json:"auto_exchange_enabled"`
}

// RIExchangeConfigUpdateRequest is the request body for PUT /api/ri-exchange/config.
type RIExchangeConfigUpdateRequest struct {
	Mode                     string  `json:"mode"`
	UtilizationThreshold     float64 `json:"utilization_threshold"`
	MaxPaymentPerExchangeUSD float64 `json:"max_payment_per_exchange_usd"`
	MaxPaymentDailyUSD       float64 `json:"max_payment_daily_usd"`
	LookbackDays             int     `json:"lookback_days"`
	AutoExchangeEnabled      bool    `json:"auto_exchange_enabled"`
}

func (r *RIExchangeConfigUpdateRequest) validate() error {
	if !config.IsValidRIExchangeMode(r.Mode) {
		return NewClientError(400, "mode must be 'manual' or 'auto'")
	}
	if err := config.ValidateRIExchangeUtilizationThreshold("utilization_threshold", r.UtilizationThreshold); err != nil {
		return NewClientError(400, err.Error())
	}
	if r.LookbackDays < 1 || r.LookbackDays > 365 {
		return NewClientError(400, "lookback_days must be between 1 and 365")
	}
	if r.MaxPaymentPerExchangeUSD < 0 {
		return NewClientError(400, "max_payment_per_exchange_usd must be >= 0")
	}
	if r.MaxPaymentDailyUSD < 0 {
		return NewClientError(400, "max_payment_daily_usd must be >= 0")
	}
	return nil
}

// RIExchangeHistoryResponse is the response for GET /api/ri-exchange/history.
type RIExchangeHistoryResponse struct {
	Records []config.RIExchangeRecord `json:"records"`
}
