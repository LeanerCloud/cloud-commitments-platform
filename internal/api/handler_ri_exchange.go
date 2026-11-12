package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/smithy-go"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

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
