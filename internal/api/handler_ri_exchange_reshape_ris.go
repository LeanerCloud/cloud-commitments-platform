package api

import (
	"context"
	"fmt"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// listRIsAccount resolves the AWS account the convertible RIs are read from
// (the deployment's ambient credentials, via STS) and applies the ?account_id=
// chip filter (issue #871). With a filter, an STS failure fails closed and a
// different account is out of scope. Without one the account only labels the
// rows (#1692), so a failure is logged and the label is left empty.
func (h *Handler) listRIsAccount(ctx context.Context, requested string) (accountID string, inScope bool, err error) {
	resolve := h.resolveAWSAccountID
	if h.riInstancesAccountResolver != nil {
		resolve = h.riInstancesAccountResolver
	}
	running, resolveErr := resolve(ctx)
	if requested == "" {
		if resolveErr != nil {
			logging.Warnf("listConvertibleRIs: could not resolve the running AWS account: %v", resolveErr)
			return "", true, nil
		}
		return running, true, nil
	}
	if resolveErr != nil {
		return "", false, fmt.Errorf("failed to resolve running AWS account for RI scope: %w", resolveErr)
	}
	return running, running == requested, nil
}

// listConvertibleRIs returns all active convertible Reserved Instances for
// the running AWS account.
//
// The optional ?account_id= query parameter narrows the listing to a single
// AWS account so the page honors the Main Header global account filter
// (issue #871). Convertible RIs are read from the deployment's ambient AWS
// credentials, which resolve to exactly one account number; when the chip
// selects a different account, none of these RIs belong to it, so we return
// an empty list rather than the unscoped fleet. A real STS failure fails
// closed (returns an error) instead of silently leaking the ambient account's
// RIs under another account's filter.
func (h *Handler) listConvertibleRIs(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope: a restricted user must
	// only see RIs for the deployment's registered AWS account.
	if inScope, sErr := h.reshapeCloudAccountInScope(ctx, session); sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &ConvertibleRIsResponse{Instances: []convertibleRIWithAccount{}}, nil
	}

	runningAccountID, inScope, err := h.listRIsAccount(ctx, req.QueryStringParameters["account_id"])
	if err != nil {
		return nil, err
	} else if !inScope {
		return &ConvertibleRIsResponse{Instances: []convertibleRIWithAccount{}}, nil
	}

	region := req.QueryStringParameters["region"]
	cfg, err := h.loadAWSConfigWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	ec2Client := h.buildReshapeEC2Client(cfg)
	instances, err := ec2Client.ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}

	out := make([]convertibleRIWithAccount, len(instances))
	for i := range instances {
		out[i] = convertibleRIWithAccount{ec2ConvertibleRI: instances[i], AccountID: runningAccountID}
	}
	return &ConvertibleRIsResponse{Instances: out}, nil
}

// getRIUtilization returns per-RI utilization from Cost Explorer.
func (h *Handler) getRIUtilization(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "view", "purchases")
	if err != nil {
		return nil, err
	}

	// Validate input before resolving the account, so a malformed request
	// makes no STS or account lookup.
	lookbackDays, err := parseLookbackDaysParam(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}

	// Apply the session's allowed_accounts scope. The resolved account also
	// keys the utilization cache, so two accounts never share a cached row.
	cloudAccountID, inScope, sErr := h.resolveReshapeAccountScope(ctx, session)
	if sErr != nil {
		return nil, sErr
	} else if !inScope {
		return &RIUtilizationResponse{Utilization: []riUtilization{}}, nil
	}

	region := req.QueryStringParameters["region"]
	cfg, err := h.loadAWSConfigWithRegion(ctx, region)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}
	// Adopt the resolved region when the caller omits ?region= so the CE
	// utilization filter and the cache key both scope to the region the
	// AWS client is actually talking to (mirrors getReshapeRecommendations).
	if region == "" {
		region = cfg.Region
	}

	recsAdapter := h.buildReshapeRecsClient(cfg)
	fetch := func(fetchCtx context.Context, days int) ([]riUtilization, error) {
		return recsAdapter.GetRIUtilization(fetchCtx, days, region)
	}
	cacheKey := config.RIUtilizationCacheKey{CloudAccountID: cloudAccountID, Region: region, LookbackDays: lookbackDays}
	utilization, err := h.getRIUtilizationCache().getOrFetch(ctx, cacheKey, riUtilizationCacheTTL, riUtilizationCacheStaleTTL, fetch)
	if err != nil {
		return nil, fmt.Errorf("failed to get RI utilization: %w", err)
	}

	utilization, err = h.restrictUtilizationToDeploymentRIs(ctx, session, cfg, utilization)
	if err != nil {
		return nil, err
	}

	return &RIUtilizationResponse{Utilization: utilization}, nil
}

// restrictUtilizationToDeploymentRIs drops utilization rows that do not belong
// to the deployment account for a scoped session. Cost Explorer
// GetReservationUtilization, called from an AWS payer (management) account,
// returns every linked member account's RIs and the rows carry only the RI id,
// so the rows cannot be matched to a member account. The deployment account's
// own RIs (EC2 DescribeReservedInstances never lists members' RIs) are the
// only rows the scope gate in the caller has authorized; unknown ids are
// dropped, so an unregistered or out-of-scope member's RIs stay hidden.
// Unrestricted sessions keep every row and make no extra AWS call.
func (h *Handler) restrictUtilizationToDeploymentRIs(ctx context.Context, session *Session, cfg aws.Config, utilization []riUtilization) ([]riUtilization, error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return utilization, nil
	}
	own, err := h.buildReshapeEC2Client(cfg).ListConvertibleReservedInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list convertible RIs: %w", err)
	}
	ownIDs := make(map[string]struct{}, len(own))
	for i := range own {
		ownIDs[own[i].ReservedInstanceID] = struct{}{}
	}
	visible := make([]riUtilization, 0, len(utilization))
	for i := range utilization {
		if _, ok := ownIDs[utilization[i].ReservedInstanceID]; ok {
			visible = append(visible, utilization[i])
		}
	}
	return visible, nil
}
