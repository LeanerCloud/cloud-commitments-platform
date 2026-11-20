package purchase

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
)

// errNoEC2SPFamily fails the tranche closed when stored recommendations name
// no usable family: the tranche expires unbought instead of guessing one.
var errNoEC2SPFamily = errors.New("no stored EC2 Instance Savings Plan recommendation names a family for this account, region and term")

// ladderEC2SPFilter is the stored-recommendation read the EC2 Instance SP
// builder needs. It has no region: stored EC2 Instance SP rows carry the region
// only inside their details, so the builder matches it there. The sweep runs it before the claim, never inside its tx.
func ladderEC2SPFilter(cloudAccountID string) config.RecommendationFilter {
	return config.RecommendationFilter{
		Provider:   string(common.ProviderAWS),
		Service:    string(common.ServiceSavingsPlansEC2Instance),
		AccountIDs: []string{cloudAccountID},
	}
}

type ec2SPFamily struct {
	family string
	weight float64 // the stored rec's hourly commitment, used only as a ratio.
	hourly float64 // this family's share of the tranche.
}

// ec2SPRecMatches reports whether a stored rec is for this tranche's account,
// region, term and payment. The top-level region is empty on real rows; when set
// it must equal the ladder region.
func ec2SPRecMatches(r *config.RecommendationRecord, in LadderBuildInput, years int) bool {
	return r.Provider == string(common.ProviderAWS) && r.Service == string(common.ServiceSavingsPlansEC2Instance) &&
		(r.Region == "" || r.Region == in.Region) && r.Term == years && r.Payment == string(ladder.PaymentNoUpfront) &&
		r.CloudAccountID != nil && *r.CloudAccountID == in.CloudAccountID
}

// ec2SPFamilyOf returns the family and weight one stored rec offers, or ok
// false when it is not a usable rec for this tranche.
func ec2SPFamilyOf(r *config.RecommendationRecord, in LadderBuildInput, years int) (family string, weight float64, ok bool) {
	if !ec2SPRecMatches(r, in, years) {
		return "", 0, false
	}
	d, err := common.DecodeServiceDetailsFor(r.Service, r.Details)
	sp, isSP := d.(*common.SavingsPlanDetails)
	if err != nil || !isSP {
		return "", 0, false
	}
	family = strings.TrimSpace(sp.InstanceFamily)
	w := sp.HourlyCommitment
	switch {
	case family == "", sp.PlanType != string(sptypes.SavingsPlanTypeEc2Instance):
	case sp.Region != in.Region:
	case math.IsNaN(w), math.IsInf(w, 0), w <= 0:
	default:
		return family, w, true
	}
	return "", 0, false
}

// ec2SPFamilyWeights returns one weight per family from the stored recs that
// match the tranche's account, region, term and payment. Stored recs supply
// identity and ratios only, never amounts.
func ec2SPFamilyWeights(in LadderBuildInput, years int) []ec2SPFamily {
	byFamily := map[string]float64{}
	for i := range in.StoredRecs {
		if family, w, ok := ec2SPFamilyOf(&in.StoredRecs[i], in, years); ok {
			byFamily[family] = max(byFamily[family], w)
		}
	}
	out := make([]ec2SPFamily, 0, len(byFamily))
	for f, w := range byFamily {
		out = append(out, ec2SPFamily{family: f, weight: w})
	}
	slices.SortFunc(out, func(a, b ec2SPFamily) int { return cmp.Compare(a.family, b.family) })
	return out
}

// mergeEC2SPDust folds every part under the Savings Plans minimum into the
// largest part, which is the first of the largest ties in family order.
func mergeEC2SPDust(fams []ec2SPFamily) []ec2SPFamily {
	largest := 0
	for i := range fams {
		if fams[i].hourly > fams[largest].hourly {
			largest = i
		}
	}
	kept := make([]ec2SPFamily, 0, len(fams))
	for i, f := range fams {
		switch {
		case i == largest:
		case f.hourly < spMinHourly:
			fams[largest].hourly += f.hourly
		default:
			kept = append(kept, f)
		}
	}
	return append(kept, fams[largest])
}

// splitEC2SPHourly splits the tranche amount by weight, merges dust into the
// largest family, and floors each part to plan precision, so the parts never
// sum to more than the tranche.
func splitEC2SPHourly(total float64, fams []ec2SPFamily) ([]ec2SPFamily, error) {
	var sum float64
	for _, f := range fams {
		sum += f.weight
	}
	for i := range fams {
		fams[i].hourly = total * fams[i].weight / sum
	}
	kept := mergeEC2SPDust(fams)
	var floored float64
	for i := range kept {
		h, err := floorSPHourly(kept[i].hourly)
		if err != nil {
			return nil, err
		}
		kept[i].hourly = h
		floored += h
	}
	if floored > total {
		return nil, fmt.Errorf("family parts sum to %g, above the tranche amount %g", floored, total)
	}
	slices.SortFunc(kept, func(a, b ec2SPFamily) int { return cmp.Compare(a.family, b.family) })
	return kept, nil
}

// buildEC2InstanceSPRecs builds one EC2 Instance Savings Plan record per family.
// A tranche with a Family is family-bound and buys exactly one record; an empty
// Family is a legacy row and is split by the stored recs' weights.
// Empty OfferingID makes the client look the offering up per family and region.
func buildEC2InstanceSPRecs(in LadderBuildInput) ([]config.RecommendationRecord, error) {
	years, total, err := computeSPInputs(in)
	if err != nil {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, err)
	}
	if in.Tranche.Family != "" {
		return buildFamilyBoundEC2SPRecs(in, years, total)
	}
	fams := ec2SPFamilyWeights(in, years)
	if len(fams) == 0 {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, errNoEC2SPFamily)
	}
	parts, err := splitEC2SPHourly(total, fams)
	if err != nil {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, err)
	}
	recs := make([]config.RecommendationRecord, 0, len(parts))
	for _, p := range parts {
		rec, err := newEC2SPRec(in, years, p.family, p.hourly)
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

// newEC2SPRec builds one EC2 Instance Savings Plan record for a family.
func newEC2SPRec(in LadderBuildInput, years int, family string, hourly float64) (config.RecommendationRecord, error) {
	details, err := common.MarshalServiceDetails(&common.SavingsPlanDetails{
		PlanType:         string(sptypes.SavingsPlanTypeEc2Instance),
		InstanceFamily:   family,
		Region:           in.Region,
		HourlyCommitment: hourly,
	})
	if err != nil {
		return config.RecommendationRecord{}, err
	}
	monthly := spMonthlyCost(hourly, years)
	return config.RecommendationRecord{
		Provider:           string(common.ProviderAWS),
		Service:            string(common.ServiceSavingsPlansEC2Instance),
		Region:             in.Region,
		ResourceType:       "ec2-instance-sp",
		Details:            details,
		Count:              1,
		Term:               years,
		Payment:            string(in.Tranche.PaymentOption),
		MonthlyCost:        &monthly,
		CommitmentCurrency: "USD",
		Selected:           true,
		CloudAccountID:     &in.CloudAccountID,
	}, nil
}
