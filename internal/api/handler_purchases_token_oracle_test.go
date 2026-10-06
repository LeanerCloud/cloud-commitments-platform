package api

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// A caller with no session must get the same answer for a missing and an
// existing execution on the email-link routes, so the routes are not an
// existence oracle (issue #435).
func TestHandleRequest_TokenActions_UnauthenticatedCannotTellMissingFromExisting(t *testing.T) {
	future := time.Now().Add(time.Hour)
	hash := config.HashApprovalToken(tokenErrRawToken)

	for _, action := range []string{"approve", "cancel", "revoke"} {
		for _, token := range []string{"", tokenErrRawToken, "wrong"} {
			t.Run(action+"/token="+token, func(t *testing.T) {
				call := func(h *Handler) *events.LambdaFunctionURLResponse {
					req := tokenErrRequest(action, token)
					delete(req.Headers, "authorization")
					resp, err := h.HandleRequest(context.Background(), req)
					require.NoError(t, err)
					return resp
				}

				exec := &config.PurchaseExecution{Status: "pending", ApprovalToken: hash, ApprovalTokenExpiresAt: &future}
				existing := call(tokenErrHandler(t, exec, nil))
				missing := call(tokenErrHandler(t, nil, config.ErrNotFound))

				assert.Equal(t, 401, existing.StatusCode, existing.Body)
				assert.Equal(t, existing.StatusCode, missing.StatusCode, "missing: %s", missing.Body)
				assert.Equal(t, existing.Body, missing.Body)
			})
		}
	}
}
