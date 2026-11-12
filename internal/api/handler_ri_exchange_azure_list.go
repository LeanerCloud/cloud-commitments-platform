package api

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
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

// ExchangeableAzureRIsResponse holds the list of Azure VM reservations that
// are eligible for the cross-SKU/cross-region exchange flow.
type ExchangeableAzureRIsResponse struct {
	Reservations []azureExchangeableReservation `json:"reservations"`
}
