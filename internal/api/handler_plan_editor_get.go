package api

import (
	"context"
	"errors"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

type planEditorSnapshotReader interface {
	ReadPlanEditorSnapshot(context.Context, string) (*config.PlanEditorSnapshot, error)
}

func (r *Router) getPlanEditorHandler(ctx context.Context, req *events.LambdaFunctionURLRequest, params map[string]string) (any, error) {
	return r.h.getPlanEditor(ctx, req, params["id"])
}

func (h *Handler) getPlanEditor(ctx context.Context, req *events.LambdaFunctionURLRequest, planID string) (*PlanEditorResponse, error) {
	if err := validateUUID(planID); err != nil {
		return nil, err
	}
	session, err := h.requirePlanEditorReadSession(ctx, req)
	if err != nil {
		return nil, planEditorReadError(err)
	}
	reader, ok := h.config.(planEditorSnapshotReader)
	if !ok {
		return nil, NewClientError(503, "plan editor storage is unavailable")
	}
	snapshot, err := reader.ReadPlanEditorSnapshot(ctx, planID)
	if err != nil {
		return nil, planEditorReadError(err)
	}
	if err = h.authorizePlanEditorSnapshotRead(ctx, session, snapshot); err != nil {
		return nil, planEditorReadError(err)
	}
	evaluatedAt := time.Now().UTC()
	policy, err := h.buildPlanEditorFieldPolicy(ctx, req, session, snapshot, evaluatedAt)
	if err != nil {
		return nil, planEditorReadError(err)
	}
	history, err := buildPlanEditorHistory(snapshot.Plan, snapshot.Executions, evaluatedAt)
	if err != nil {
		return nil, planEditorReadError(err)
	}
	revision, err := snapshot.Revision()
	if err != nil {
		return nil, planEditorReadError(err)
	}
	return buildPlanEditorResponse(snapshot, policy, history, revision), nil
}

func planEditorReadError(err error) error {
	if clientErr, ok := IsClientError(err); ok {
		return clientErr
	}
	if IsNotFoundError(err) || errors.Is(err, config.ErrNotFound) {
		return errNotFound
	}
	if errors.Is(err, config.ErrPlanEditorUnsupportedSize) {
		return NewClientError(413, "plan editor history exceeds the supported size")
	}
	if errors.Is(err, config.ErrPlanEditorAttribution) || errors.Is(err, ErrPlanEditorProjectionState) || errors.Is(err, ErrPlanEditorUnsupportedSchedule) {
		return NewClientError(409, "plan editor state cannot be resolved; refresh the plan")
	}
	return NewClientError(500, "failed to load plan editor")
}
