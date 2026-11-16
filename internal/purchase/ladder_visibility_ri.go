package purchase

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// RIDescriber is the one EC2 call the RI visibility prefetch needs.
// *ec2.Client satisfies it.
type RIDescriber interface {
	DescribeReservedInstances(ctx context.Context, in *ec2.DescribeReservedInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeReservedInstancesOutput, error)
}

// LadderRIDescriberFactory returns a describer authenticated for one cloud
// account and region (the EC2 client's region scopes the lookup).
type LadderRIDescriberFactory func(ctx context.Context, cloudAccountID, region string) (RIDescriber, error)

// LadderRIDescriber is the production RI factory, built like LadderSPDescriber.
func (m *Manager) LadderRIDescriber(ctx context.Context, cloudAccountID, region string) (RIDescriber, error) {
	account, err := m.config.GetCloudAccount(ctx, cloudAccountID)
	if err != nil {
		return nil, fmt.Errorf("load cloud account %s: %w", cloudAccountID, err)
	}
	if account == nil {
		return nil, fmt.Errorf("cloud account %s not found", cloudAccountID)
	}
	provCfg, err := m.resolveAccountProvider(ctx, *account)
	if err != nil {
		return nil, err
	}
	if provCfg.AWSCredentialsProvider == nil {
		return nil, fmt.Errorf("cloud account %s resolved without AWS credentials", cloudAccountID)
	}
	return ec2.NewFromConfig(aws.Config{Region: region, Credentials: provCfg.AWSCredentialsProvider}), nil
}

// riCountsAsExisting mirrors the ladder's RI layer input (the pinned go
// ec2 ListConvertibleReservedInstances): state active, convertible offering
// class, in the ladder's region. Narrower is safe (the tranche stays fired);
// wider would complete a tranche that netting cannot see and over-buy. The
// netting sums (recurring + usage + fixed/duration) * count and reads an absent
// field as 0, so an RI missing any of them would end the tranche while adding
// $0: it stays fired instead.
func riCountsAsExisting(ri ec2types.ReservedInstances, e expectedPlan) bool {
	if ri.State != ec2types.ReservedInstanceStateActive || ri.OfferingClass != ec2types.OfferingClassTypeConvertible {
		return false
	}
	if !riPriceReported(ri) {
		return false
	}
	// A regional RI has no AZ; a zonal one must sit in the ladder's region.
	az := aws.ToString(ri.AvailabilityZone)
	return az == "" || strings.HasPrefix(az, e.region)
}

// riPriceReported requires every price field the netting reads, plus the go
// library's CostKnown rule: a positive count, and a duration whenever there is
// an upfront fee to amortize.
func riPriceReported(ri ec2types.ReservedInstances) bool {
	recurring := ri.UsagePrice != nil
	for _, rc := range ri.RecurringCharges {
		recurring = recurring || rc.Amount != nil
	}
	if ri.FixedPrice == nil || !recurring || ri.Duration == nil || ri.InstanceCount == nil {
		return false
	}
	return aws.ToInt32(ri.InstanceCount) > 0 && (aws.ToFloat32(ri.FixedPrice) == 0 || aws.ToInt64(ri.Duration) > 0)
}

// riLookup is the convertible RI lookup: per-ID DescribeReservedInstances with
// the same state and offering-class filters as the ladder's own listing. A nil
// factory fails closed.
func riLookup(factory LadderRIDescriberFactory) visibilityLookup {
	return func(ctx context.Context, account, region string, ids []string) (func(expectedPlan) bool, error) {
		if factory == nil {
			return nil, errors.New("no RI describer configured")
		}
		desc, err := factory(ctx, account, region)
		if err != nil {
			return nil, err
		}
		found := map[string]ec2types.ReservedInstances{}
		for chunk := range slices.Chunk(ids, ladderDescribeBatch) {
			res, err := desc.DescribeReservedInstances(ctx, &ec2.DescribeReservedInstancesInput{
				ReservedInstancesIds: chunk,
				Filters: []ec2types.Filter{
					{Name: aws.String("state"), Values: []string{string(ec2types.ReservedInstanceStateActive)}},
					{Name: aws.String("offering-class"), Values: []string{string(ec2types.OfferingClassTypeConvertible)}},
				},
			})
			if err != nil {
				return nil, fmt.Errorf("DescribeReservedInstances: %w", err)
			}
			for i := range res.ReservedInstances {
				found[aws.ToString(res.ReservedInstances[i].ReservedInstancesId)] = res.ReservedInstances[i]
			}
		}
		return func(p expectedPlan) bool {
			ri, ok := found[p.id]
			return ok && riCountsAsExisting(ri, p)
		}, nil
	}
}
