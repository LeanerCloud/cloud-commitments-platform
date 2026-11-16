package purchase

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderRITermYears is the only term the RI layer buys: 1y, no upfront.
const ladderRITermYears = 1

// riMaxCount bounds a derived count so a corrupt price cannot ask for millions.
const riMaxCount = 10000

var errNoRIFamily = errors.New("no stored EC2 recommendation names a regional instance type for this account, region and 1y no-upfront")

// RIUnit is the live price of ONE reservation of an instance shape, looked up
// before the claim. Err is set when the lookup or its validation failed.
type RIUnit struct {
	Err    error
	Hourly float64
}

type riFamily struct {
	details *common.ComputeDetails
	key     string
	weight  float64 // the stored rec's monthly cost, used only as a ratio.
}

func riKey(d *common.ComputeDetails) string {
	return strings.Join([]string{d.InstanceType, d.Platform, d.Tenancy, d.Scope}, "|")
}

// riRecMatches reports whether a stored rec is for this account and region at
// 1y no-upfront. A rec without a monthly cost has no weight and is ignored.
func riRecMatches(r *config.RecommendationRecord, account, region string) bool {
	return r.Provider == string(common.ProviderAWS) && r.Service == string(common.ServiceEC2) && r.Region == region &&
		r.Term == ladderRITermYears && r.Payment == string(common.PaymentNoUpfront) &&
		r.CloudAccountID != nil && *r.CloudAccountID == account && r.MonthlyCost != nil
}

func riUsable(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) && x > 0 }

// riFamilyOf returns the shape and weight one stored rec offers, or ok false.
// Stored recs give identity and ratios only, never amounts or counts.
func riFamilyOf(r *config.RecommendationRecord, account, region string) (riFamily, bool) {
	if !riRecMatches(r, account, region) {
		return riFamily{}, false
	}
	d, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	cd, ok := d.(*common.ComputeDetails)
	if err != nil || !ok || cd.InstanceType == "" || cd.Platform == "" || cd.Tenancy == "" || cd.Scope != "regional" {
		return riFamily{}, false
	}
	if w := *r.MonthlyCost; riUsable(w) {
		return riFamily{key: riKey(cd), details: cd, weight: w}, true
	}
	return riFamily{}, false
}

// riFamilies dedupes matching stored recs by shape (largest weight wins) in key order.
func riFamilies(stored []config.RecommendationRecord, account, region string) []riFamily {
	byKey := map[string]riFamily{}
	for i := range stored {
		if f, ok := riFamilyOf(&stored[i], account, region); ok && f.weight > byKey[f.key].weight {
			byKey[f.key] = f
		}
	}
	out := make([]riFamily, 0, len(byKey))
	for _, f := range byKey {
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b riFamily) int { return cmp.Compare(a.key, b.key) })
	return out
}

// riCount floors share/unit to whole reservations, never rounding up.
func riCount(share, unit float64) (int, error) {
	if !riUsable(unit) {
		return 0, fmt.Errorf("offering price %g is not a positive number", unit)
	}
	n := math.Floor(share / unit)
	if math.IsNaN(n) || n > riMaxCount {
		return 0, fmt.Errorf("derived count %g is out of range", n)
	}
	for n > 0 && n*unit > share {
		n--
	}
	return int(n), nil
}

// riShapeRecord sizes one shape from its share of the tranche: a nil record
// means the share cannot buy one reservation. Any shape without a valid live
// price is an error, which fails the whole tranche closed.
func riShapeRecord(in LadderBuildInput, f riFamily, share float64) (*config.RecommendationRecord, float64, error) {
	unit, ok := in.RIUnits[f.key]
	if !ok || unit.Err != nil {
		return nil, 0, fmt.Errorf("no usable offering price for %s: %w", f.key, cmp.Or(unit.Err, errors.New("not looked up")))
	}
	n, err := riCount(share, unit.Hourly)
	if err != nil || n == 0 {
		return nil, 0, err
	}
	rec, err := riRecord(in, f, n, unit.Hourly)
	return &rec, float64(n) * unit.Hourly, err
}

// buildConvertibleRIRecs builds one EC2 record per stored shape, sized as
// floor(share / live unit price). A shape too small for one reservation buys
// nothing and its share is not given to another shape.
func buildConvertibleRIRecs(in LadderBuildInput) ([]config.RecommendationRecord, error) {
	recs, err := riRecs(in)
	if err != nil {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, err)
	}
	return recs, nil
}

// riTrancheAmount validates the tranche (1y no-upfront only) and returns its
// hourly amount, floored to plan precision.
func riTrancheAmount(in LadderBuildInput) (float64, error) {
	years, total, err := computeSPInputs(in)
	if err == nil && years != ladderRITermYears {
		err = fmt.Errorf("the RI layer buys %dy terms only, got %dy", ladderRITermYears, years)
	}
	return total, err
}

func riRecs(in LadderBuildInput) ([]config.RecommendationRecord, error) {
	total, err := riTrancheAmount(in)
	if err != nil {
		return nil, err
	}
	fams := riFamilies(in.StoredRecs, in.CloudAccountID, in.Region)
	if len(fams) == 0 {
		return nil, errNoRIFamily
	}
	var weights, spent float64
	for _, f := range fams {
		weights += f.weight
	}
	if !riUsable(weights) {
		return nil, errors.New("stored weights overflow")
	}
	var recs []config.RecommendationRecord
	for _, f := range fams {
		rec, cost, err := riShapeRecord(in, f, total*f.weight/weights)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			recs, spent = append(recs, *rec), spent+cost
		}
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("amount %g is too small for one reservation of any stored shape", total)
	}
	// Unreachable while riCount holds count*unit <= share; kept as defense in depth.
	if spent > total {
		return nil, fmt.Errorf("reservations cost %g/hr, above the tranche amount %g", spent, total)
	}
	return recs, nil
}

func riRecord(in LadderBuildInput, f riFamily, count int, unitHourly float64) (config.RecommendationRecord, error) {
	details, err := common.MarshalServiceDetails(f.details)
	if err != nil {
		return config.RecommendationRecord{}, err
	}
	monthly := float64(count) * unitHourly * common.HoursPerMonth
	return config.RecommendationRecord{
		Provider:           string(common.ProviderAWS),
		Service:            string(common.ServiceEC2),
		Region:             in.Region,
		ResourceType:       f.details.InstanceType,
		Details:            details,
		Count:              count,
		Term:               ladderRITermYears,
		Payment:            string(in.Tranche.PaymentOption),
		MonthlyCost:        &monthly,
		CommitmentCurrency: "USD",
		Selected:           true,
		CloudAccountID:     &in.CloudAccountID,
	}, nil
}
