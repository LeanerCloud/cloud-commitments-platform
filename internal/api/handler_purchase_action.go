package api

import (
	"context"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/aws/aws-lambda-go/events"
)

func (h *Handler) requireSessionPurchaseAction(ctx context.Context, req *events.LambdaFunctionURLRequest, anyAction, ownAction string) (*Session, string, error) {
	principal, err := h.requireAuth(ctx, req)
	if err != nil {
		return nil, "", err
	}
	session, err := h.requireSessionPrincipal(contextWithPrincipal(ctx, principal), req)
	if err != nil {
		return nil, "", err
	}
	if principal.Kind == PrincipalUserAPIKey && principal.UserID != session.UserID {
		return nil, "", NewClientError(403, "API key and bearer session must belong to the same user")
	}
	for _, action := range []string{anyAction, ownAction} {
		effective, permErr := h.requirePrincipalPermission(ctx, req, principal, action, auth.ResourcePurchases)
		if permErr == nil {
			effective.Email = session.Email
			return effective, action, nil
		}
		if ce, ok := IsClientError(permErr); !ok || ce.code != 403 {
			return nil, "", permErr
		}
	}
	return nil, "", NewClientError(403, "permission denied: requires "+anyAction+" or "+ownAction+" on purchases")
}

func knownScopeValue(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []string{value}
}
