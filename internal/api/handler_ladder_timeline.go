package api

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) ladderTimelineScope(ctx context.Context, req *events.LambdaFunctionURLRequest) (store config.LadderTimelineStore, accountID, provider string, cursor *config.LadderTimelineCursor, err error) {
	session, err := h.requirePermission(ctx, req, string(auth.ActionView), string(auth.ResourceConfig))
	if err != nil {
		return nil, "", "", nil, err
	}
	params := req.QueryStringParameters
	accountID, provider = params["account_id"], params["provider"]
	if accountID == "" || provider == "" {
		return nil, "", "", nil, NewClientError(400, "account_id and provider are required")
	}
	account, err := h.requireAccountAccess(ctx, session, accountID)
	if err != nil {
		return nil, "", "", nil, err
	}
	if account.Provider != provider {
		return nil, "", "", nil, NewClientError(404, "account not found")
	}
	if err = h.requirePermissionConstraints(ctx, session, auth.ActionView, auth.ResourceConfig, []auth.PermissionConstraints{{
		AccountIDs: []string{accountID}, Providers: []string{provider}, StrictScope: true,
	}}); err != nil {
		return nil, "", "", nil, err
	}
	if common.ProviderType(provider) != common.ProviderAWS {
		return nil, "", "", nil, NewClientError(501, "Ladder planning is currently available only for AWS accounts")
	}
	cursor, err = ladderTimelineCursor(params)
	if err != nil {
		return nil, "", "", nil, err
	}
	store, ok := h.config.(config.LadderTimelineStore)
	if !ok {
		return nil, "", "", nil, NewClientError(503, "Ladder timeline storage is unavailable")
	}
	return store, accountID, provider, cursor, nil
}

func ladderTimelineCursor(params map[string]string) (*config.LadderTimelineCursor, error) {
	created, id := params["after_created_at"], params["after_id"]
	if created == "" && id == "" {
		return nil, nil
	}
	stamp, err := time.Parse(time.RFC3339Nano, created)
	if err != nil || stamp.IsZero() {
		return nil, NewClientError(400, "after_created_at must be a valid RFC3339 timestamp")
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, NewClientError(400, "after_id must be a UUID")
	}
	return &config.LadderTimelineCursor{CreatedAt: stamp, ID: id}, nil
}

func (h *Handler) getLadderTimeline(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	store, accountID, provider, cursor, err := h.ladderTimelineScope(ctx, req)
	if err != nil {
		return nil, err
	}
	return store.ListLadderTimeline(ctx, accountID, provider, cursor)
}

func (h *Handler) getLadderTimelineRuns(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	store, accountID, provider, cursor, err := h.ladderTimelineScope(ctx, req)
	if err != nil {
		return nil, err
	}
	return store.ListLadderTimelineRuns(ctx, accountID, provider, cursor)
}
