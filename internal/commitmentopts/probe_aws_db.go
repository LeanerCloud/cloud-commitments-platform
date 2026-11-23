package commitmentopts

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/opensearch"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// ---------------------------------------------------------------------------
// RDS
// ---------------------------------------------------------------------------

// RDSDescribeOfferings is the minimal RDS surface the probe needs. It
// matches the method signature on the generated client so tests can
// substitute a mock without dragging in the full RDSAPI interface.
type RDSDescribeOfferings interface {
	DescribeReservedDBInstancesOfferings(ctx context.Context, params *rds.DescribeReservedDBInstancesOfferingsInput, optFns ...func(*rds.Options)) (*rds.DescribeReservedDBInstancesOfferingsOutput, error)
}

// RDSProber probes rds:DescribeReservedDBInstancesOfferings.
type RDSProber struct {
	// NewClient builds a client from the probe's aws.Config. Override in
	// tests to return a mock.
	NewClient func(cfg aws.Config) RDSDescribeOfferings
}

// Service returns "rds".
func (p *RDSProber) Service() string { return "rds" }

// Probe returns the normalized (term, payment) combos RDS currently sells
// against db.t3.micro.
func (p *RDSProber) Probe(ctx context.Context, cfg aws.Config) ([]Combo, error) {
	client := p.client(cfg)
	raw, err := walkPaginated(ctx, p.Service(), func(ctx context.Context, token *string) ([]rawOffer, *string, error) {
		out, err := client.DescribeReservedDBInstancesOfferings(ctx, &rds.DescribeReservedDBInstancesOfferingsInput{
			DBInstanceClass: aws.String(probeTargetRDS),
			MaxRecords:      aws.Int32(pageSize),
			Marker:          token,
		})
		if err != nil {
			return nil, nil, err
		}
		offers := make([]rawOffer, 0, len(out.ReservedDBInstancesOfferings))
		for _, o := range out.ReservedDBInstancesOfferings {
			offers = append(offers, rawOffer{
				durationSeconds: int64(aws.ToInt32(o.Duration)),
				payment:         aws.ToString(o.OfferingType),
			})
		}
		return offers, out.Marker, nil
	})
	if err != nil {
		return nil, err
	}
	return collect(p.Service(), raw), nil
}

func (p *RDSProber) client(cfg aws.Config) RDSDescribeOfferings {
	if p.NewClient != nil {
		return p.NewClient(cfg)
	}
	return rds.NewFromConfig(cfg)
}

// ---------------------------------------------------------------------------
// ElastiCache
// ---------------------------------------------------------------------------

// ElastiCacheDescribeOfferings is the minimal ElastiCache surface we use.
type ElastiCacheDescribeOfferings interface {
	DescribeReservedCacheNodesOfferings(ctx context.Context, params *elasticache.DescribeReservedCacheNodesOfferingsInput, optFns ...func(*elasticache.Options)) (*elasticache.DescribeReservedCacheNodesOfferingsOutput, error)
}

// ElastiCacheProber probes elasticache:DescribeReservedCacheNodesOfferings.
type ElastiCacheProber struct {
	NewClient func(cfg aws.Config) ElastiCacheDescribeOfferings
}

// Service returns "elasticache".
func (p *ElastiCacheProber) Service() string { return "elasticache" }

// Probe returns the combos for cache.t3.micro.
func (p *ElastiCacheProber) Probe(ctx context.Context, cfg aws.Config) ([]Combo, error) {
	client := p.client(cfg)
	raw, err := walkPaginated(ctx, p.Service(), func(ctx context.Context, token *string) ([]rawOffer, *string, error) {
		out, err := client.DescribeReservedCacheNodesOfferings(ctx, &elasticache.DescribeReservedCacheNodesOfferingsInput{
			CacheNodeType: aws.String(probeTargetElastiCache),
			MaxRecords:    aws.Int32(pageSize),
			Marker:        token,
		})
		if err != nil {
			return nil, nil, err
		}
		offers := make([]rawOffer, 0, len(out.ReservedCacheNodesOfferings))
		for _, o := range out.ReservedCacheNodesOfferings {
			offers = append(offers, rawOffer{
				durationSeconds: int64(aws.ToInt32(o.Duration)),
				payment:         aws.ToString(o.OfferingType),
			})
		}
		return offers, out.Marker, nil
	})
	if err != nil {
		return nil, err
	}
	return collect(p.Service(), raw), nil
}

func (p *ElastiCacheProber) client(cfg aws.Config) ElastiCacheDescribeOfferings {
	if p.NewClient != nil {
		return p.NewClient(cfg)
	}
	return elasticache.NewFromConfig(cfg)
}

// ---------------------------------------------------------------------------
// OpenSearch
// ---------------------------------------------------------------------------

// OpenSearchDescribeOfferings is the minimal OpenSearch surface we use.
// The OpenSearch API has no per-instance-type filter on this endpoint, so
// the probe filters client-side after fetching.
type OpenSearchDescribeOfferings interface {
	DescribeReservedInstanceOfferings(ctx context.Context, params *opensearch.DescribeReservedInstanceOfferingsInput, optFns ...func(*opensearch.Options)) (*opensearch.DescribeReservedInstanceOfferingsOutput, error)
}

// OpenSearchProber probes opensearch:DescribeReservedInstanceOfferings.
type OpenSearchProber struct {
	NewClient func(cfg aws.Config) OpenSearchDescribeOfferings
}

// Service returns "opensearch".
func (p *OpenSearchProber) Service() string { return "opensearch" }

// Probe returns the combos for t3.small.search.
func (p *OpenSearchProber) Probe(ctx context.Context, cfg aws.Config) ([]Combo, error) {
	client := p.client(cfg)
	raw, capped, err := walkPaginatedCapped(ctx, p.Service(), func(ctx context.Context, token *string) ([]rawOffer, *string, error) {
		out, err := client.DescribeReservedInstanceOfferings(ctx, &opensearch.DescribeReservedInstanceOfferingsInput{
			MaxResults: pageSize,
			NextToken:  token,
		})
		if err != nil {
			return nil, nil, err
		}
		offers := make([]rawOffer, 0, len(out.ReservedInstanceOfferings))
		for _, o := range out.ReservedInstanceOfferings {
			if string(o.InstanceType) != probeTargetOpenSearch {
				continue
			}
			offers = append(offers, rawOffer{
				durationSeconds: int64(o.Duration),
				payment:         string(o.PaymentOption),
			})
		}
		return offers, out.NextToken, nil
	})
	if err != nil {
		return nil, err
	}
	return collectUnlessCapped(p.Service(), raw, capped), nil
}

func (p *OpenSearchProber) client(cfg aws.Config) OpenSearchDescribeOfferings {
	if p.NewClient != nil {
		return p.NewClient(cfg)
	}
	return opensearch.NewFromConfig(cfg)
}
