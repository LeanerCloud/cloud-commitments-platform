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
	accounts, err := h.riExchangeDirectAccounts(ctx)
	if err != nil {
		return nil, err
	}
	allowed, err := h.visibleRIExchangeDirectPairs(ctx, session, store, accounts)
	if mapped := mapRIExchangeResolveError(err); mapped != nil {
		return nil, mapped
	}
	filter.Allowed = allowed
	page, err := store.ListUnresolvedRIExchangeDirectExecutions(ctx, filter)
	if mapped := mapRIExchangeResolveError(err); mapped != nil {
		return nil, mapped
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
func (h *Handler) visibleRIExchangeDirectPairs(ctx context.Context, session *Session, store config.RIExchangeDirectListStore, accounts map[string]config.CloudAccount) ([]config.RIExchangeDirectAccountRegion, error) {
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
		account, inScope := riExchangeDirectPairAccount(scope, accounts, p)
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

// riExchangeDirectAccounts loads the registered AWS accounts by ID, so the scope
// check can also match an account's AWS account number.
func (h *Handler) riExchangeDirectAccounts(ctx context.Context) (map[string]config.CloudAccount, error) {
	provider := string(common.ProviderAWS)
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{Provider: &provider})
	if err != nil {
		return nil, fmt.Errorf("failed to list cloud accounts: %w", err)
	}
	byID := make(map[string]config.CloudAccount, len(accounts))
	for i := range accounts {
		byID[accounts[i].ID] = accounts[i]
	}
	return byID, nil
}

// riExchangeDirectPairAccount returns the account the grant is checked
// against (the unattributed sentinel for a row with no registered account) and
// whether the session's account scope covers it. A row with no account needs
// an unrestricted scope. A group may list an account by its ID or by its AWS
// account number (the group write keeps an external id verbatim), so either
// form admits the row; external ids are unique per provider, so the number
// cannot name another AWS account.
func riExchangeDirectPairAccount(scope auth.AccountScope, accounts map[string]config.CloudAccount, p config.RIExchangeDirectAccountRegion) (string, bool) {
	if p.CloudAccountID == nil {
		return unattributedAccountConstraint, scope.AllowsAll()
	}
	id := *p.CloudAccountID
	if scope.Allows(id, "") {
		return id, true
	}
	external := accounts[id].ExternalID
	return id, external != "" && scope.Allows(external, "")
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
