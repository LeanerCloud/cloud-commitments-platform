package api

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/ctxutil"
)

// reshapeEC2Client is the narrow slice of the EC2 client that
// getReshapeRecommendations needs. Scoped to this handler so mocks
// stay small. The concrete EC2 client that newReshapeEC2Client
// (provider_wiring.go) builds for the nil-factory fallback path
// implements it.
//
// Cross-family alternatives no longer flow through here — they're
// sourced from the cached AWS Cost Explorer purchase recommendations
// in Postgres via purchaseRecLookupFromStore (see exchange_lookup.go),
// so the EC2 client only needs to enumerate convertible RIs and the
// region's offered instance types (DescribeInstanceTypeOfferings), which
// limit reshape targets to sizes the region actually sells.
type reshapeEC2Client interface {
	ListConvertibleReservedInstances(ctx context.Context) ([]ec2ConvertibleRI, error)
	GetValidResourceTypes(ctx context.Context) ([]string, error)
}

// reshapeRecsClient is the narrow slice of the recommendations
// adapter that getReshapeRecommendations needs (the utilization
// fetcher injected into the cache wrapper). Scoped identically.
type reshapeRecsClient interface {
	GetRIUtilization(ctx context.Context, lookbackDays int, region string) ([]riUtilization, error)
}

// buildReshapeEC2Client honors the injected factory when set, falling
// back to the direct AWS SDK constructor otherwise. Tests inject a
// stub via Handler.reshapeEC2Factory; prod leaves the field nil.
func (h *Handler) buildReshapeEC2Client(cfg aws.Config) reshapeEC2Client {
	if h.reshapeEC2Factory != nil {
		return h.reshapeEC2Factory(cfg)
	}
	return newReshapeEC2Client(cfg)
}

// buildReshapeRecsClient mirrors buildReshapeEC2Client for the
// recommendations adapter.
func (h *Handler) buildReshapeRecsClient(cfg aws.Config) reshapeRecsClient {
	if h.reshapeRecsFactory != nil {
		return h.reshapeRecsFactory(cfg)
	}
	return newReshapeRecsClient(cfg)
}

// awsBaseConfigLoadTimeout bounds the one-time base AWS config load. The load
// reads the environment and, where no region is configured, may ask the
// instance metadata service; ten seconds leaves room for the SDK's own IMDS
// retries while staying inside the server's 30s per-request cap.
const awsBaseConfigLoadTimeout = 10 * time.Second

// getBaseAWSConfig returns the cached base AWS config, loading it once via
// sync.Once. Every request-path user of awsCfg goes through here.
//
// The once caches its error with the config, so the load must not run on the
// calling request's context: a request canceled or timed out mid-load would
// otherwise seal "context canceled" into awsCfgErr for the life of the
// process (issue #104, audit finding A02-017). It runs detached from the
// request's cancellation, bounded by awsBaseConfigLoadTimeout.
func (h *Handler) getBaseAWSConfig(ctx context.Context) (aws.Config, error) {
	h.awsCfgOnce.Do(func() {
		loadCtx, cancel := ctxutil.DetachedWithTimeout(ctx, awsBaseConfigLoadTimeout)
		defer cancel()
		load := h.loadAWSCfg
		if load == nil {
			load = loadDefaultAWSConfig
		}
		h.awsCfg, h.awsCfgErr = load(loadCtx)
	})
	return h.awsCfg, h.awsCfgErr
}

// loadAWSConfigWithRegion returns the cached base config, optionally overriding the region.
func (h *Handler) loadAWSConfigWithRegion(ctx context.Context, region string) (aws.Config, error) {
	cfg, err := h.getBaseAWSConfig(ctx)
	if err != nil {
		return aws.Config{}, err
	}
	if region != "" {
		cfg.Region = region
	}
	return cfg, nil
}
