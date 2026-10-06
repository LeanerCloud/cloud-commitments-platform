package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// A caller with no session must get the same answer for a missing and an
// existing execution on the email-link routes, so the routes are not an
// existence oracle (issue #435).
func TestHandleRequest_TokenActions_UnauthenticatedCannotTellMissingFromExisting(t *testing.T) {
	future := time.Now().Add(time.Hour)
	hash := config.HashApprovalToken(tokenErrRawToken)

	headerVariants := map[string]map[string]string{
		"no-header":      {},
		"invalid-bearer": {"authorization": "Bearer bad-tok"},
		"bogus-x-auth":   {"x-authorization": "Basic bad-tok"},
	}
	for _, action := range []string{"approve", "cancel", "revoke"} {
		for _, token := range []string{"", tokenErrRawToken, "wrong"} {
			for variant, headers := range headerVariants {
				t.Run(action+"/token="+token+"/"+variant, func(t *testing.T) {
					call := func(h *Handler) *events.LambdaFunctionURLResponse {
						req := tokenErrRequest(action, token)
						req.Headers = headers
						resp, err := h.HandleRequest(context.Background(), req)
						require.NoError(t, err)
						return resp
					}

					exec := &config.PurchaseExecution{Status: "pending", ApprovalToken: hash, ApprovalTokenExpiresAt: &future}
					existingH := tokenErrHandler(t, exec, nil)
					missingH := tokenErrHandler(t, nil, config.ErrNotFound)
					for _, h := range []*Handler{existingH, missingH} {
						h.auth.(*MockAuthService).On("ValidateSession", mock.Anything, "bad-tok").Return(nil, errors.New("invalid session")).Maybe()
					}
					existing := call(existingH)
					missing := call(missingH)

					assert.Equal(t, 401, existing.StatusCode, existing.Body)
					assert.Equal(t, existing.StatusCode, missing.StatusCode, "missing: %s", missing.Body)
					assert.Equal(t, existing.Body, missing.Body)
				})
			}
		}
	}
}
