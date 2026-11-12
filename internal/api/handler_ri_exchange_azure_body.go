package api

import (
	"fmt"
	"strings"
)

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
