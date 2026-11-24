package commitmentopts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
)

// azureSpService is the service name written into Combo.Service for Azure
// Savings Plans. Matches the key used in the Options map.
const azureSpService = "savingsplans"

// azureSpProvider is the provider string for Azure commitment combos.
const azureSpProvider = "azure"

// azureSKUComputeSavingsPlan is the ARM SKU name Azure accepts in the
// ValidatePurchase body for a compute Savings Plan. The bare "Compute" name
// the probe used to send is not a SKU Azure knows (cloud-commitments-go
// savingsplans maps its "Compute" plan type to this value before purchase).
const azureSKUComputeSavingsPlan = "Compute_Savings_Plan"

// azureOptionalTermYears is the one term Azure does not sell everywhere. A
// client 4xx on a combo of this term drops just that combo; the same answer
// for a term Azure really sells (1 and 3 years) is a transient or wrong-scope
// failure and aborts the probe, because a persisted success row never expires
// and would keep the sold combo missing for good.
const azureOptionalTermYears = 5

// azureProbeHourlyCommitment is the minimum non-zero hourly commitment used
// in ValidatePurchase probe requests. The value itself is irrelevant for
// structural validation (the API checks term + payment shape, not the dollar
// amount), but zero is rejected server-side so we use the documented minimum.
const azureProbeHourlyCommitment float64 = 0.001

// azureCurrencyCodePattern matches a 3-letter ISO 4217 currency code. It
// mirrors the check the cloud-commitments-go Azure Savings Plans provider
// applies to SavingsPlanDetails.Currency before purchase or validation.
var azureCurrencyCodePattern = regexp.MustCompile(`^[A-Za-z]{3}$`)

// azureCandidateCombos lists every (term, billingPlan) pair that the Azure
// Savings Plans API publishes. The probe calls ValidatePurchase for each one
// and retains only those the API accepts as structurally valid.
//
// Azure SP payment model:
//   - nil BillingPlan   = full upfront ("upfront")
//   - BillingPlanP1M    = monthly installments ("monthly")
//
// The payment names are the Azure vocabulary config.ValidPaymentOptions
// accepts ("upfront", "monthly"); "all-upfront" is AWS-only.
//
// Azure currently offers 1-year and 3-year terms for Compute SPs; P5Y is
// defined in the SDK constants but is not sold in practice. The probe
// includes P5Y and drops it if ValidatePurchase rejects it, so the persisted
// combos always reflect live API reality rather than the SDK enum.
var azureCandidateCombos = []struct {
	azurePlan   *armbillingbenefits.BillingPlan
	azureTerm   armbillingbenefits.Term
	paymentName string
	termYears   int
}{
	{termYears: 1, azureTerm: armbillingbenefits.TermP1Y, azurePlan: nil, paymentName: "upfront"},
	{termYears: 1, azureTerm: armbillingbenefits.TermP1Y, azurePlan: billingPlanP1M(), paymentName: "monthly"},
	{termYears: 3, azureTerm: armbillingbenefits.TermP3Y, azurePlan: nil, paymentName: "upfront"},
	{termYears: 3, azureTerm: armbillingbenefits.TermP3Y, azurePlan: billingPlanP1M(), paymentName: "monthly"},
	{termYears: 5, azureTerm: armbillingbenefits.TermP5Y, azurePlan: nil, paymentName: "upfront"},
	{termYears: 5, azureTerm: armbillingbenefits.TermP5Y, azurePlan: billingPlanP1M(), paymentName: "monthly"},
}

// billingPlanP1M returns a pointer to BillingPlanP1M. Using a function avoids
// taking the address of an unaddressable constant.
func billingPlanP1M() *armbillingbenefits.BillingPlan {
	p := armbillingbenefits.BillingPlanP1M
	return &p
}

// AzureSPValidateAPI is the minimal Azure Billing Benefits surface the probe
// needs. It matches the ValidatePurchase method on *armbillingbenefits.RPClient
// so tests can substitute a mock without importing the concrete SDK client.
//
// NOTE: providers/azure/services/savingsplans.RPValidateAPI defines the same
// one-method interface. The duplication is intentional: importing the provider
// package from internal/commitmentopts would create a circular dependency. The
// interface is tiny enough that duplicating it here is the cleanest option.
type AzureSPValidateAPI interface {
	ValidatePurchase(
		ctx context.Context,
		body armbillingbenefits.SavingsPlanPurchaseValidateRequest,
		options *armbillingbenefits.RPClientValidatePurchaseOptions,
	) (armbillingbenefits.RPClientValidatePurchaseResponse, error)
}

// AzureSPProber probes the Azure Billing Benefits ValidatePurchase endpoint for
// each candidate (term, payment) combination and returns those the API accepts
// as live Combos.
//
// Design notes:
//   - Azure has no "list offerings" catalog endpoint comparable to AWS
//     Describe*Offerings. ValidatePurchase is the closest live signal that a
//     given (term, billingPlan) configuration is currently accepted.
//   - The probe validates against the caller's real subscription with the
//     minimum non-zero hourly commitment; ValidatePurchase never creates a
//     purchase, so no billing impact occurs.
//   - A 4xx answer (other than 401, 403, 404, 408 and 429) for a 5-year combo means
//     Azure does not sell it: the combo is dropped and the probe continues, but a
//     probe that ends with no accepted combo at all is an error. Any
//     other error (a 4xx on a 1- or 3-year combo, auth, throttling, 5xx, network, canceled context) is a
//     probe failure and is bubbled up so the Service can apply its
//     all-or-nothing policy.
type AzureSPProber struct {
	// NewClient builds an AzureSPValidateAPI from a credential. Override
	// in tests to return a mock.
	NewClient func(cred azcore.TokenCredential) (AzureSPValidateAPI, error)
}

// Service returns "savingsplans".
func (p *AzureSPProber) Service() string { return azureSpService }

// ProbeAzure probes the Azure ValidatePurchase endpoint for each candidate
// (term, billingPlan) combo and returns Combos for those the API accepts.
//
// subscriptionID is the Azure subscription the probe is scoped to; it must be
// a real one (an enabled Azure account's), never a placeholder, so an empty
// value fails closed before any client is built.
//
// currencyCode is the billing currency of the account the probe runs
// against (a 3-letter ISO 4217 code such as "USD" or "EUR"). Azure
// evaluates a ValidatePurchase request against the billing account's
// currency, so a probe sent in any other currency yields a verdict about a
// commitment the account could never buy. The caller must resolve the
// account's real currency; there is no USD default. An empty or malformed
// code fails closed before any client is built or any request is sent.
//
// The method signature differs from the AWS Prober interface because Azure
// authentication uses azcore.TokenCredential rather than aws.Config. Callers
// use this method directly; Service.probeAndPersistAzure wires it up.
func (p *AzureSPProber) ProbeAzure(ctx context.Context, cred azcore.TokenCredential, subscriptionID, currencyCode string) ([]Combo, error) {
	if subscriptionID == "" {
		return nil, errors.New("savingsplans: probe requires a subscription ID")
	}
	currency, err := azureProbeCurrency(currencyCode)
	if err != nil {
		return nil, err
	}

	client, err := p.client(cred)
	if err != nil {
		return nil, fmt.Errorf("savingsplans: create validate client: %w", err)
	}

	var combos []Combo
	for _, c := range azureCandidateCombos {
		ok, err := p.probeCombo(ctx, client, subscriptionID, c.azureTerm, c.azurePlan, currency, c.termYears == azureOptionalTermYears)
		if err != nil {
			// Treat non-validation errors (auth, network, 5xx) as probe
			// failures — they prevent us from knowing whether the combo
			// is valid, so we must not silently drop it.
			return nil, fmt.Errorf("savingsplans: probe %dy %s: %w", c.termYears, c.paymentName, err)
		}
		if ok {
			combos = append(combos, Combo{
				Provider:  azureSpProvider,
				Service:   azureSpService,
				TermYears: c.termYears,
				Payment:   c.paymentName,
			})
		}
	}
	if len(combos) == 0 {
		// Persisting an empty snapshot would mark the cache warm for good, so a
		// wrong account (bad subscription, currency mismatch) would never be
		// retried. Treat "nothing accepted" as a failed probe instead.
		return nil, errors.New("savingsplans: no candidate combo was accepted; refusing to record an empty snapshot")
	}
	return combos, nil
}

// azureProbeCurrency validates and upper-cases the caller-supplied billing
// currency. It never substitutes a default: an unknown currency is an error.
func azureProbeCurrency(code string) (string, error) {
	if !azureCurrencyCodePattern.MatchString(code) {
		return "", fmt.Errorf("savingsplans: probe currency %q is not a 3-letter currency code; the billing account's currency must be resolved before probing", code)
	}
	return strings.ToUpper(code), nil
}

// isComboRejection reports whether err is Azure answering that it does not
// sell the validated combination: an HTTP 4xx response that is not about the
// caller's identity (401, 403), a missing scope (404), a timeout (408) or
// throttling (429), which say nothing about the combo and must abort the probe
// instead. ProbeAzure still fails when every combo is rejected, so a 400 that
// really means a wrong account cannot become an empty snapshot.
func isComboRejection(err error) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	switch respErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	default:
		return respErr.StatusCode >= 400 && respErr.StatusCode < 500
	}
}

// probeCombo calls ValidatePurchase for a single (term, billingPlan) pair and
// returns true if the API considers it a valid offering.
//
// A response where any benefit has Valid=false, or where no benefit carries
// Valid=true, is treated as "not offered" (returns false, nil). Any other API
// error, including a client 4xx when rejectionDrops is false, is returned as-is so the caller can decide whether it is a transient
// failure or a configuration problem.
func (p *AzureSPProber) probeCombo(
	ctx context.Context,
	client AzureSPValidateAPI,
	subscriptionID string,
	term armbillingbenefits.Term,
	billingPlan *armbillingbenefits.BillingPlan,
	currencyCode string,
	rejectionDrops bool,
) (bool, error) {
	billingScopeID := fmt.Sprintf("/subscriptions/%s", subscriptionID)
	grain := armbillingbenefits.CommitmentGrainHourly
	appliedScope := armbillingbenefits.AppliedScopeTypeShared
	hourlyAmount := azureProbeHourlyCommitment
	displayName := "cudly-probe"
	skuName := azureSKUComputeSavingsPlan

	props := &armbillingbenefits.SavingsPlanOrderAliasProperties{
		DisplayName:      &displayName,
		BillingScopeID:   &billingScopeID,
		Term:             &term,
		AppliedScopeType: &appliedScope,
		Commitment: &armbillingbenefits.Commitment{
			Amount:       &hourlyAmount,
			CurrencyCode: &currencyCode,
			Grain:        &grain,
		},
	}
	if billingPlan != nil {
		props.BillingPlan = billingPlan
	}

	body := armbillingbenefits.SavingsPlanPurchaseValidateRequest{
		Benefits: []*armbillingbenefits.SavingsPlanOrderAliasModel{
			{
				SKU:        &armbillingbenefits.SKU{Name: &skuName},
				Properties: props,
			},
		},
	}

	resp, err := client.ValidatePurchase(ctx, body, nil)
	if err != nil {
		if rejectionDrops && isComboRejection(err) {
			return false, nil
		}
		return false, err
	}

	// The API returns a per-benefit valid flag. The combo counts as sold only
	// on positive confirmation: at least one benefit with Valid=true and none
	// with Valid=false. An empty Benefits slice or a nil flag says nothing, so
	// it must not record a combo (such as a P5Y plan) as purchasable
	// (issue #243, audit finding A05-012).
	confirmed := false
	for _, b := range resp.Benefits {
		if b == nil || b.Valid == nil {
			continue
		}
		if !*b.Valid {
			return false, nil
		}
		confirmed = true
	}
	return confirmed, nil
}

func (p *AzureSPProber) client(cred azcore.TokenCredential) (AzureSPValidateAPI, error) {
	if p.NewClient != nil {
		return p.NewClient(cred)
	}
	c, err := armbillingbenefits.NewRPClient(cred, nil)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// DefaultAzureProbers returns one prober instance for the Azure Savings Plans
// service. The Service wires these up by default for Azure probe runs.
func DefaultAzureProbers() []*AzureSPProber {
	return []*AzureSPProber{
		{},
	}
}
