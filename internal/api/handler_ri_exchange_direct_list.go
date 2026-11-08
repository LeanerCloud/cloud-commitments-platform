package api

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) listRIExchangeDirectExecutions(ctx context.Context, req *events.LambdaFunctionURLRequest) (any, error) {
	session, err := h.requirePermission(ctx, req, "execute", "ri-exchange")
	if err != nil {
		return nil, err
	}
	filter, err := parseRIExchangeDirectListQuery(req.QueryStringParameters)
	if err != nil {
		return nil, err
	}
	store, ok := h.config.(config.RIExchangeDirectListStore)
	if !ok {
		return nil, NewClientError(503, "RI exchange resolve storage is unavailable")
	}
	allowed, err := h.visibleRIExchangeDirectPairs(ctx, session, store)
	if err != nil {
		return nil, err
	}
	filter.Allowed = allowed
	page, err := store.ListUnresolvedRIExchangeDirectExecutions(ctx, filter)
	if err := mapRIExchangeResolveError(err); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	resp := riExchangeDirectListResponse{Executions: make([]riExchangeDirectListItem, 0, len(page.Rows))}
	for i := range page.Rows {
		resp.Executions = append(resp.Executions, newRIExchangeDirectListItem(&page.Rows[i], now))
	}
	if page.Next != nil {
		resp.NextCursor = encodeRIExchangeDirectCursor(page.Next)
	}
	return resp, nil
}

// visibleRIExchangeDirectPairs keeps the (account, region) pairs of unresolved
// rows that the session may see: its account scope allows the account (a row
// with no registered account needs an unrestricted scope) and the grant on
// execute:ri-exchange covers the same account, provider, service and region
// the direct execute checks. The grant is evaluated by the authorization
// service, never reimplemented here, so OR'd grants, a user API key's own
// narrower permissions and region spelling behave as they do on execute.
func (h *Handler) visibleRIExchangeDirectPairs(ctx context.Context, session *Session, store config.RIExchangeDirectListStore) ([]config.RIExchangeDirectAccountRegion, error) {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	pairs, err := store.ListUnresolvedRIExchangeDirectAccountRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list unresolved direct exchange accounts: %w", err)
	}
	var visible []config.RIExchangeDirectAccountRegion
	for _, p := range pairs {
		account, inScope := riExchangeDirectPairAccount(scope, p)
		if !inScope {
			continue
		}
		granted, err := h.riExchangeDirectPairGranted(ctx, session, account, p.Region)
		if err != nil {
			return nil, err
		}
		if granted {
			visible = append(visible, p)
		}
	}
	return visible, nil
}

// riExchangeDirectPairAccount returns the account the grant is checked
// against (the unattributed sentinel for a row with no registered account) and
// whether the session's account scope covers it. A row with no account needs
// an unrestricted scope.
func riExchangeDirectPairAccount(scope auth.AccountScope, p config.RIExchangeDirectAccountRegion) (string, bool) {
	if p.CloudAccountID == nil {
		return unattributedAccountConstraint, scope.AllowsAll()
	}
	return *p.CloudAccountID, scope.Allows(*p.CloudAccountID, "")
}

// riExchangeDirectPairGranted asks the authorization service whether the
// grant covers an AWS EC2 exchange in the account and region. A denial is
// false; any other failure is an error.
func (h *Handler) riExchangeDirectPairGranted(ctx context.Context, session *Session, account, region string) (bool, error) {
	err := h.requirePermissionConstraints(ctx, session, "execute", "ri-exchange", []auth.PermissionConstraints{{
		AccountIDs: []string{account},
		Providers:  []string{string(common.ProviderAWS)},
		Services:   []string{string(common.ServiceEC2)},
		Regions:    []string{region},
	}})
	if ce, isClient := IsClientError(err); isClient && ce.code == 403 {
		return false, nil
	}
	return err == nil, err
}
