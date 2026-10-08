package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/google/uuid"
)

func (h *Handler) amendLadderTranche(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	store, accountID, provider, session, err := h.authorizeLadderAmend(ctx, req, id)
	if err != nil {
		return nil, err
	}
	amendment, err := decodeLadderAmendment(req.Body)
	if err != nil {
		return nil, err
	}
	result, err := store.AmendLadderTranche(ctx, id, accountID, provider, session.UserID, amendment)
	switch {
	case errors.Is(err, config.ErrLadderAmendNotFound):
		return nil, errNotFound
	case errors.Is(err, config.ErrLadderAmendConflict):
		return nil, NewClientError(409, err.Error())
	case errors.Is(err, config.ErrLadderAmendInvalid):
		return nil, NewClientError(400, err.Error())
	default:
		return result, err
	}
}

func (h *Handler) authorizeLadderAmend(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (store config.LadderAmendmentStore, accountID, provider string, session *Session, err error) {
	session, err = h.requirePermission(ctx, req, string(auth.ActionUpdate), string(auth.ResourceConfig))
	if err != nil {
		return nil, "", "", nil, err
	}
	if _, err = uuid.Parse(id); err != nil {
		return nil, "", "", nil, errNotFound
	}
	store, ok := h.config.(config.LadderAmendmentStore)
	if !ok {
		return nil, "", "", nil, NewClientError(503, "Ladder amendment storage is unavailable")
	}
	accountID, provider, err = store.LadderTrancheScope(ctx, id)
	if errors.Is(err, config.ErrLadderAmendNotFound) {
		return nil, "", "", nil, errNotFound
	}
	if err != nil {
		return nil, "", "", nil, err
	}
	if _, err = h.requireAccountAccess(ctx, session, accountID); err != nil {
		return nil, "", "", nil, err
	}
	if err = h.requirePermissionConstraints(ctx, session, auth.ActionUpdate, auth.ResourceConfig, []auth.PermissionConstraints{{
		AccountIDs: []string{accountID}, Providers: []string{provider}, StrictScope: true,
	}}); err != nil {
		return nil, "", "", nil, err
	}
	if common.ProviderType(provider) != common.ProviderAWS {
		return nil, "", "", nil, NewClientError(501, "Ladder planning is currently available only for AWS accounts")
	}
	return store, accountID, provider, session, nil
}

func decodeLadderAmendment(body string) (config.LadderAmendment, error) {
	var amendment config.LadderAmendment
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&amendment); err != nil {
		return amendment, NewClientError(400, "Invalid ladder amendment")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return amendment, NewClientError(400, "Expected one ladder amendment")
	}
	if err := amendment.Validate(); err != nil {
		return amendment, NewClientError(400, err.Error())
	}
	if !amendment.ScheduledDate.After(time.Now()) {
		return amendment, NewClientError(400, "scheduled_date must be in the future")
	}
	return amendment, nil
}
