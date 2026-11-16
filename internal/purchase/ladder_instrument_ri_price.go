package purchase

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// RIOfferingPricer is the one read-only EC2 call the RI builder needs.
// The go library's EC2 client satisfies it.
type RIOfferingPricer interface {
	GetOfferingDetailsForClass(ctx context.Context, rec common.Recommendation, offeringClass string) (*common.OfferingDetails, error)
}

// LadderRIPricerFactory returns a pricer for one cloud account and region.
type LadderRIPricerFactory func(ctx context.Context, cloudAccountID, region string) (RIOfferingPricer, error)

func ladderRIFilter(cloudAccountID, region string) config.RecommendationFilter {
	return config.RecommendationFilter{
		Provider: string(common.ProviderAWS), Service: string(common.ServiceEC2), Region: region, AccountIDs: []string{cloudAccountID},
	}
}

// riPriceRecommendation is the lookup rec: the shape at 1y no-upfront.
func riPriceRecommendation(f riFamily, account, region string) common.Recommendation {
	d := *f.details
	return common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceEC2, Account: account, Region: region,
		ResourceType: d.InstanceType, CommitmentType: common.CommitmentReservedInstance,
		Term: common.Term1Year, PaymentOption: common.PaymentNoUpfront, Count: 1, Details: &d,
	}
}

// riUnitOf prices one reservation: recurring plus upfront spread over the
// term. A no-upfront offering with an upfront fee, another shape or another
// currency is refused instead of guessed at.
func riUnitOf(f riFamily, o *common.OfferingDetails) RIUnit {
	switch {
	case o == nil:
		return RIUnit{Err: errors.New("offering lookup returned nothing")}
	case o.ResourceType != f.details.InstanceType:
		return RIUnit{Err: fmt.Errorf("offering is for %q, not %q", o.ResourceType, f.details.InstanceType)}
	case o.Currency != "USD":
		return RIUnit{Err: fmt.Errorf("offering currency %q is not USD", o.Currency)}
	case o.Term != string(common.Term1Year):
		return RIUnit{Err: fmt.Errorf("offering term %q is not %q", o.Term, common.Term1Year)}
	case o.PaymentOption != string(ec2types.OfferingTypeValuesNoUpfront):
		return RIUnit{Err: fmt.Errorf("offering payment option %q is not no-upfront", o.PaymentOption)}
	case o.UpfrontCost != 0:
		return RIUnit{Err: fmt.Errorf("no-upfront offering reports an upfront cost of %g", o.UpfrontCost)}
	}
	return RIUnit{Hourly: o.RecurringCost}
}

// readRIEvidence reads stored EC2 recs and prices each candidate shape, before
// the claim. A store error errors the config; a pricer or lookup error is
// recorded per shape so only RI tranches turn unresolved.
func readRIEvidence(ctx context.Context, deps LadderFireDeps, cfg *config.LadderConfigDB) ([]config.RecommendationRecord, map[string]RIUnit, error) {
	stored, err := deps.Store.ListStoredRecommendations(ctx, ladderRIFilter(cfg.CloudAccountID, deps.Region))
	if err != nil {
		return nil, nil, fmt.Errorf("read stored EC2 recommendations: %w", err)
	}
	units := map[string]RIUnit{}
	fams := riFamilies(stored, cfg.CloudAccountID, deps.Region)
	var pricer RIOfferingPricer
	pricerErr := errors.New("no RI offering pricer is configured")
	if deps.RIPricer != nil && len(fams) > 0 {
		pricer, pricerErr = deps.RIPricer(ctx, cfg.CloudAccountID, deps.Region)
	}
	for _, f := range fams {
		if pricerErr != nil {
			units[f.key] = RIUnit{Err: pricerErr}
			continue
		}
		o, lookupErr := pricer.GetOfferingDetailsForClass(ctx, riPriceRecommendation(f, cfg.CloudAccountID, deps.Region), "convertible")
		if lookupErr != nil {
			units[f.key] = RIUnit{Err: lookupErr}
			continue
		}
		units[f.key] = riUnitOf(f, o)
	}
	return stored, units, nil
}
