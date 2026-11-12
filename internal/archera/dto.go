package archera

import (
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"
)

// ErrUnrepresentable is returned when a vendor number has no exact finite
// decimal form (the DTO never rounds or converts to float).
var ErrUnrepresentable = errors.New("archera value has no exact decimal representation")

// ErrCredentialMaterial withholds a comparison containing the configured credential.
var ErrCredentialMaterial = errors.New("archera comparison contains credential material")

const (
	dtoTitle         = "Archera commitment plan comparison (read-only)"
	currencyNote     = "currency not provided by Archera"
	unknownVerdict   = "unknown"
	maxVendorStrLen  = 256
	offerNameCaveat  = "Archera's name for this offer; a plan comparison is a hypothetical rollup, not a bindable quote, and an insured target is subject to Archera underwriting allowances, not a guarantee."
	basisNote        = "Monthly figures are 730-hour monthly rates; the Archera premium is already included in commitment cost totals; upfront figures are one-time amounts and are never summed with monthly rates."
	deltaBasisNote   = "Hypothetical deltas compare against the current plan, not against on-demand."
	supportedSummary = "supported"
)

// Decimal renders r as the shortest exact decimal string. nil (unknown) stays
// nil. The denominator of a reduced rational is finite in decimal exactly when
// it has no prime factor other than 2 and 5, and the digits needed equal
// max(power of 2, power of 5).
func Decimal(r *big.Rat) (*string, error) {
	if r == nil {
		return nil, nil
	}
	d := new(big.Int).Set(r.Denom())
	two, five, zero := big.NewInt(2), big.NewInt(5), new(big.Int)
	count := func(p *big.Int) int {
		n, m := 0, new(big.Int)
		for {
			q, rem := new(big.Int).QuoRem(d, p, m)
			if rem.Cmp(zero) != 0 {
				return n
			}
			d, n = q, n+1
		}
	}
	c2, c5 := count(two), count(five)
	if d.Cmp(big.NewInt(1)) != 0 {
		return nil, ErrUnrepresentable
	}
	s := r.FloatString(max(c2, c5))
	return &s, nil
}

// CleanString removes control, format and line/paragraph-separator characters
// from vendor text and caps it at 256 bytes on a rune boundary.
func CleanString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxVendorStrLen {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ProductSupportDTO is the documentation-derived verdict for one offer.
// Source and Evidence appear only for a supported verdict; allowance and
// customer eligibility are always unknown.
type ProductSupportDTO struct {
	Status              string `json:"status"`
	Source              string `json:"source,omitempty"`
	Evidence            string `json:"evidence,omitempty"`
	UnderwritingAllowed string `json:"underwriting_allowance"`
	CustomerEligibility string `json:"customer_eligibility"`
}

// FinancialsDTO carries the 730-hour monthly rate block.
type FinancialsDTO struct {
	CommitmentCostTotal *string `json:"monthly_730h_commitment_cost_total"`
	CloudProviderCost   *string `json:"monthly_730h_cloud_provider_cost"`
	Premium             *string `json:"monthly_730h_archera_premium"`
	GrossSavings        *string `json:"monthly_730h_gross_savings"`
	NetSavings          *string `json:"monthly_730h_net_savings"`
	CoveredOnDemandCost *string `json:"monthly_730h_covered_on_demand_cost"`
}

// TotalsDTO is a plan-wide rollup.
type TotalsDTO struct {
	FinancialsDTO
	UpfrontCost *string `json:"upfront_one_time_cost"`
}

// DeltaDTO is the vendor's candidate-minus-current difference.
type DeltaDTO struct {
	MonthlyNetSavings *string `json:"monthly_730h_net_savings"`
	UpfrontCost       *string `json:"upfront_one_time_cost"`
	DiscountRate      *string `json:"discount_rate"`
	BreakevenDays     *string `json:"breakeven_days"`
}

// HypotheticalDeltaDTO compares a hypothetical rollup against the current plan.
type HypotheticalDeltaDTO struct {
	MonthlyNetSavings     *string `json:"monthly_730h_net_savings"`
	MonthlyCommitmentCost *string `json:"monthly_730h_commitment_cost"`
	UpfrontCost           *string `json:"upfront_one_time_cost"`
}

// LineItemDTO is how one line item lands inside a hypothetical rollup.
type LineItemDTO struct {
	LineItemID           string  `json:"line_item_id"`
	Reason               string  `json:"reason"`
	ActualTerm           *string `json:"actual_term"`
	ActualPaymentOption  *string `json:"actual_payment_option"`
	ActualCommitmentType *string `json:"actual_commitment_type"`
}

// HypotheticalDTO is a rollup for one (term, payment option) combination.
type HypotheticalDTO struct {
	ContractTerm   *string              `json:"contract_term"`
	PaymentOption  string               `json:"payment_option"`
	Totals         TotalsDTO            `json:"totals"`
	DeltaVsCurrent HypotheticalDeltaDTO `json:"delta_vs_current"`
	LineItems      []LineItemDTO        `json:"line_items"`
}

// OfferDTO is one offer (current or candidate) for a line item.
type OfferDTO struct {
	OfferID              string            `json:"offer_id"`
	IsCurrent            bool              `json:"is_current"`
	Provider             string            `json:"provider"`
	CommitmentType       string            `json:"commitment_type"`
	Region               *string           `json:"region"`
	ContractTerm         *string           `json:"contract_term"`
	PaymentOption        *string           `json:"payment_option"`
	LeaseAttached        bool              `json:"lease_attached"`
	LeaseMenuItemID      *string           `json:"lease_menu_item_id"`
	ArcheraOfferName     *string           `json:"archera_offer_name,omitempty"`
	ArcheraOfferNameNote string            `json:"archera_offer_name_note,omitempty"`
	ProductSupport       ProductSupportDTO `json:"archera_product_support"`
	DiscountRate         *string           `json:"discount_rate"`
	BreakevenDays        *string           `json:"breakeven_days"`
	Monthly              FinancialsDTO     `json:"monthly"`
	UpfrontCost          *string           `json:"upfront_one_time_cost"`
	DeltaVsCurrent       DeltaDTO          `json:"delta_vs_current"`
}

// RowDTO is a line item's current offer plus its alternatives.
type RowDTO struct {
	LineItemID string     `json:"line_item_id"`
	Current    OfferDTO   `json:"current"`
	Candidates []OfferDTO `json:"candidates"`
}

// ComparisonDTO is the platform response. No org ID is included.
type ComparisonDTO struct {
	Title                 string            `json:"title"`
	PlanID                string            `json:"plan_id"`
	FetchedAt             string            `json:"fetched_at"`
	Currency              *string           `json:"currency"`
	CurrencyNote          string            `json:"currency_note"`
	PremiumIncluded       bool              `json:"premium_included"`
	BasisNote             string            `json:"basis_note"`
	DeltaBasisNote        string            `json:"delta_basis_note"`
	Current               TotalsDTO         `json:"current"`
	Hypotheticals         []HypotheticalDTO `json:"hypotheticals"`
	Rows                  []RowDTO          `json:"rows"`
	NonGatingDisclosure   string            `json:"non_gating_disclosure"`
	SponsorshipDisclosure string            `json:"sponsorship_disclosure"`
}

type mapper struct {
	err error
	key insurance.Secret
}

// Mirrors cloud-commitments-mcp/tools/archera_comparison.go's archeraSkeleton policy.
func credentialSkeleton(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case utf8.RuneError, 0x115F, 0x1160, 0x3164, 0xFFA0:
			continue
		}
		if !unicode.In(r, unicode.Cc, unicode.Cf, unicode.Mn, unicode.Me, unicode.Zl, unicode.Zp, unicode.Zs) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (m *mapper) check(s string) {
	key := credentialSkeleton(strings.TrimSpace(m.key.Reveal()))
	if m.err == nil && key != "" && strings.Contains(credentialSkeleton(s), key) {
		m.err = ErrCredentialMaterial
	}
}

func (m *mapper) clean(s string) string {
	m.check(s)
	return CleanString(s)
}

func (m *mapper) ptr(s *string) *string {
	if s != nil {
		m.check(*s)
	}
	return cleanPtr(s)
}

func (m *mapper) payment(p *insurance.PaymentOption) *string {
	if p != nil {
		m.check(string(*p))
	}
	return payment(p)
}

func (m *mapper) dec(r *big.Rat) *string {
	s, err := Decimal(r)
	if err != nil && m.err == nil {
		m.err = err
	}
	if s != nil {
		m.check(*s)
	}
	return s
}

func cleanPtr(s *string) *string {
	if s == nil {
		return nil
	}
	c := CleanString(*s)
	return &c
}

func (m *mapper) financials(f insurance.Financials) FinancialsDTO {
	return FinancialsDTO{
		CommitmentCostTotal: m.dec(f.CommitmentCostTotal), CloudProviderCost: m.dec(f.CloudProviderCost),
		Premium: m.dec(f.Premium), GrossSavings: m.dec(f.GrossSavings), NetSavings: m.dec(f.NetSavings),
		CoveredOnDemandCost: m.dec(f.CoveredOnDemandCost),
	}
}

func (m *mapper) totals(t insurance.Totals) TotalsDTO {
	return TotalsDTO{FinancialsDTO: m.financials(t.Monthly), UpfrontCost: m.dec(t.UpfrontCost)}
}

func (m *mapper) delta(d insurance.OfferDelta) DeltaDTO {
	return DeltaDTO{MonthlyNetSavings: m.dec(d.MonthlyNetSavings), UpfrontCost: m.dec(d.UpfrontCost),
		DiscountRate: m.dec(d.DiscountRate), BreakevenDays: m.dec(d.BreakevenDays)}
}

func payment(p *insurance.PaymentOption) *string {
	if p == nil {
		return nil
	}
	s := CleanString(string(*p))
	return &s
}

func (m *mapper) offer(e *insurance.OfferEntry) OfferDTO {
	support := insurance.AssessProductSupport(e.Provider, e.CommitmentType)
	ps := ProductSupportDTO{Status: unknownVerdict, UnderwritingAllowed: unknownVerdict, CustomerEligibility: unknownVerdict}
	if support.Status == insurance.ProductSupportSupported {
		ps.Status, ps.Source, ps.Evidence = supportedSummary, support.Source, support.Evidence
	}
	o := OfferDTO{
		OfferID: m.clean(e.OfferID), IsCurrent: e.IsCurrent, Provider: m.clean(string(e.Provider)),
		CommitmentType: m.clean(e.CommitmentType), Region: m.ptr(e.Region), ContractTerm: m.ptr(e.ContractTerm),
		PaymentOption: m.payment(e.PaymentOption), LeaseAttached: e.LeaseBacked(), LeaseMenuItemID: m.ptr(e.LeaseMenuItemID),
		ProductSupport: ps, DiscountRate: m.dec(e.DiscountRate), BreakevenDays: m.dec(e.BreakevenDays),
		Monthly: m.financials(e.Monthly), UpfrontCost: m.dec(e.UpfrontCost), DeltaVsCurrent: m.delta(e.Delta),
	}
	if e.GuaranteedDisplayName != nil {
		o.ArcheraOfferName, o.ArcheraOfferNameNote = m.ptr(e.GuaranteedDisplayName), offerNameCaveat
	}
	return o
}

// BuildComparison maps the contract type to the platform DTO. Money is exact
// decimal text or null; every vendor string is cleaned and capped. It returns
// ErrUnrepresentable when any number has no exact decimal form.
func BuildComparison(c *insurance.Comparison, key insurance.Secret) (*ComparisonDTO, error) {
	m := &mapper{key: key}
	out := &ComparisonDTO{
		Title: dtoTitle, PlanID: m.clean(c.PlanID), FetchedAt: c.FetchedAt.UTC().Format(time.RFC3339),
		CurrencyNote: currencyNote, PremiumIncluded: true, BasisNote: basisNote, DeltaBasisNote: deltaBasisNote,
		Current:       m.totals(c.Current),
		Hypotheticals: []HypotheticalDTO{}, Rows: []RowDTO{},
		NonGatingDisclosure: common.ArcheraNonGatingDisclosure, SponsorshipDisclosure: common.ArcheraSponsorshipDisclosure,
	}
	for i := range c.Hypotheticals {
		h := &c.Hypotheticals[i]
		hd := HypotheticalDTO{
			ContractTerm: m.ptr(h.ContractTerm), PaymentOption: m.clean(string(h.PaymentOption)),
			Totals: m.totals(h.Totals), LineItems: []LineItemDTO{},
			DeltaVsCurrent: HypotheticalDeltaDTO{MonthlyNetSavings: m.dec(h.DeltaMonthlyNetSavings),
				MonthlyCommitmentCost: m.dec(h.DeltaMonthlyCommitmentCost), UpfrontCost: m.dec(h.DeltaUpfrontCost)},
		}
		for _, li := range h.LineItems {
			hd.LineItems = append(hd.LineItems, LineItemDTO{
				LineItemID: m.clean(li.LineItemID), Reason: m.clean(string(li.Reason)), ActualTerm: m.ptr(li.ActualTerm),
				ActualPaymentOption: m.payment(li.ActualPaymentOption), ActualCommitmentType: m.ptr(li.ActualCommitmentType),
			})
		}
		out.Hypotheticals = append(out.Hypotheticals, hd)
	}
	for i := range c.Rows {
		r := &c.Rows[i]
		rd := RowDTO{LineItemID: m.clean(r.LineItemID), Current: m.offer(&r.Current), Candidates: []OfferDTO{}}
		for j := range r.Candidates {
			rd.Candidates = append(rd.Candidates, m.offer(&r.Candidates[j]))
		}
		out.Rows = append(out.Rows, rd)
	}
	if m.err != nil {
		return nil, m.err
	}
	return out, nil
}
