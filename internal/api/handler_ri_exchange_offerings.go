package api

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// targetOfferingsEC2Client is the narrow EC2 interface that
// listTargetOfferings needs. Scoped here so tests can inject a tiny
// stub without implementing the full concrete EC2 client surface.
type targetOfferingsEC2Client interface {
	ListConvertibleReservedInstances(ctx context.Context) ([]ec2ConvertibleRI, error)
	ListTargetOfferings(ctx context.Context, params ec2ListTargetOfferingsParams) ([]ec2TargetOffering, error)
}

// buildTargetOfferingsEC2Client honors the injected factory when set,
// falling back to the direct AWS SDK constructor otherwise.
func (h *Handler) buildTargetOfferingsEC2Client(cfg aws.Config) targetOfferingsEC2Client {
	if h.targetOfferingsEC2Factory != nil {
		return h.targetOfferingsEC2Factory(cfg)
	}
	return newTargetOfferingsEC2Client(cfg)
}

// TargetOfferingsResponse is the response for
// GET /api/ri-exchange/target-offerings.
type TargetOfferingsResponse struct {
	Offerings []ec2TargetOffering `json:"offerings"`
}

// offeringIDPattern matches a standard AWS offering UUID used for
// ReservedInstancesOfferingId values. Used both as a server-side guard
// (Defect 2) and to reject any stray free-text before it reaches AWS.
// Mirrored in frontend/src/riexchange.ts offeringUUIDPattern; keep the two in sync.
var offeringIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// listTargetOfferings returns valid convertible RI exchange target
// offerings for the source RI identified by ?source_ri_id=<uuid>.
//
// The handler looks up the source RI from DescribeReservedInstances,
// extracts its ProductDescription / Tenancy / Scope / Duration,
// and passes those to the EC2 client's ListTargetOfferings, which
// calls DescribeReservedInstancesOfferings with the same typed-field
// shape used by PR #690. Instance type is intentionally omitted from
// the query so AWS returns all valid target instance types -- the full
// menu of what the user can exchange into.
//
// GET /api/ri-exchange/target-offerings?source_ri_id=<uuid>&region=<region>.
func (h *Handler) listTargetOfferings(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	if visErr := h.requireReshapeAccountVisible(ctx, session); visErr != nil {
		return nil, visErr
	}

	sourceRIID := req.QueryStringParameters["source_ri_id"]
	if sourceRIID == "" {
		return nil, NewClientError(400, "source_ri_id is required")
	}

	region := req.QueryStringParameters["region"]
	cfg, err := h.loadAWSConfigWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := h.buildTargetOfferingsEC2Client(cfg)

	// Fetch all convertible RIs to locate the source RI's attributes.
	// DescribeReservedInstances does not support a single-ID filter
	// without the full ARN, so we enumerate and filter by ID.
	ris, err := ec2Client.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}

	var sourceRI *ec2ConvertibleRI
	for i := range ris {
		if ris[i].ReservedInstanceID == sourceRIID {
			sourceRI = &ris[i]
			break
		}
	}
	if sourceRI == nil {
		return nil, NewClientError(404, fmt.Sprintf("source RI %q not found in region %s", sourceRIID, cfg.Region))
	}

	offerings, err := targetOfferingsForSource(ctx, ec2Client, sourceRI)
	if err != nil {
		return nil, err
	}

	return &TargetOfferingsResponse{Offerings: offerings}, nil
}

// targetOfferingsForSource lists the exchange targets for sourceRI, keeping a
// zonal RI's targets in its own AZ. A zone refusal (the source RI's scope and
// AZ disagree) is a 422.
func targetOfferingsForSource(ctx context.Context, ec2Client targetOfferingsEC2Client, sourceRI *ec2ConvertibleRI) ([]ec2TargetOffering, error) {
	offerings, err := ec2Client.ListTargetOfferings(ctx, ec2ListTargetOfferingsParams{
		ProductDescription: sourceRI.ProductDescription,
		Tenancy:            sourceRI.InstanceTenancy,
		Scope:              sourceRI.Scope,
		AvailabilityZone:   sourceRI.AvailabilityZone,
		Duration:           sourceRI.Duration,
	})
	if err != nil {
		if isOfferingZoneRefusal(err) {
			return nil, NewClientError(422, fmt.Sprintf("source RI %q: %v", sourceRI.ReservedInstanceID, err))
		}
		return nil, fmt.Errorf("failed to list target offerings: %w", err)
	}
	return offerings, nil
}

// offeringZoneRefusals are the messages the EC2 provider returns when it
// refuses a zone-inconsistent offering lookup before calling AWS: a zonal RI
// without an AZ, or a regional RI that names one. The provider exports no
// sentinel, so these are matched by text; the target-offerings tests run the
// real provider client so a reworded message fails them.
var offeringZoneRefusals = []string{
	"zonal RI offering needs an availability zone",
	"regional RI offering must not name an availability zone",
}

// isOfferingZoneRefusal reports whether err is the provider's zone refusal.
// It is the source RI's data that is inconsistent, so the caller maps it to a
// 4xx rather than a 500.
func isOfferingZoneRefusal(err error) bool {
	msg := err.Error()
	for _, r := range offeringZoneRefusals {
		if strings.Contains(msg, r) {
			return true
		}
	}
	return false
}
