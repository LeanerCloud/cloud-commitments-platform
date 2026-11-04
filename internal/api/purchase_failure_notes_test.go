package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeConnectErr = "failed to connect to `host=db.internal.example user=cudly_app database=cudly`: " +
	"dial error (dial tcp 10.0.0.5:5432: connect: connection refused)"

func assertNoConnectText(t *testing.T, where, s string) {
	t.Helper()
	for _, n := range []string{"db.internal.example", "cudly_app", "10.0.0.5", "dial tcp"} {
		assert.NotContains(t, s, n, where)
	}
}

func TestTailedNotesWithStoreErrors_NeverReachUserSurfaces(t *testing.T) {
	notes := []string{
		"ramp not advanced: " + fakeConnectErr,
		"refusing to execute: ladder tranche lookup failed: " + fakeConnectErr,
		"refusing to execute ladder purchase: global config unreadable: " + fakeConnectErr,
		purchase.ErrStaleAzurePricing.Error() + ": cannot read the latest collection state to confirm Azure prices are current: " + fakeConnectErr,
	}
	for _, note := range notes {
		name := note[:strings.Index(note, ":")]
		t.Run(name, func(t *testing.T) {
			row := executionToHistoryRow(config.PurchaseExecution{ExecutionID: "e-1", Status: "failed", Error: note}, "", "")
			assertNoConnectText(t, "status_description", row.StatusDescription)
			assert.NotEmpty(t, row.StatusDescription)

			exec := &config.PurchaseExecution{ExecutionID: "e-1", Status: "failed", Error: note}
			body, err := json.Marshal(buildPurchaseDetailsResponse(exec, "plan"))
			require.NoError(t, err)
			assertNoConnectText(t, "details", string(body))

			creator := retryCallerID
			failed := &config.PurchaseExecution{ExecutionID: retryExecID, Status: "failed", CreatedByUserID: &creator,
				Error: "FROM_EMAIL not configured for this deployment; " + note}
			handler, _, _ := buildSessionRetryHandler(t, failed, &Session{UserID: retryCallerID}, false, true)
			_, rErr := handler.retryPurchase(t.Context(), sessionRetryReq(), retryExecID)
			ce, ok := IsClientError(rErr)
			require.True(t, ok)
			assert.Equal(t, 409, ce.code)
			raw, mErr := json.Marshal(ce.Details())
			require.NoError(t, mErr)
			assertNoConnectText(t, "retry 409", string(raw)+ce.Error())
			assert.NotEmpty(t, ce.Details()["failure_reason"])
		})
	}
}
