package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgcommon "github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	awsladder "github.com/LeanerCloud/cloud-commitments-go/providers/aws/ladder"
)

// f64ptr returns a pointer to the given float64. BufferReshapeConfig uses
// *float64 caps to distinguish "unlimited" (nil) from an explicit value.
func f64ptr(v float64) *float64 { return &v }

// TestWireLadderWriteSide_ExecutionDisabled_RealAWSLadder_RefusesWithoutAWSCall is
// the direct spend-prevention test for the ladder_execution_enabled kill-switch.
//
// Unlike the handler tests (which exercise wireLadderWriteSide's no-op branch with
// a non-AWSLadder fake), this constructs a REAL *awsladder.AWSLadder via the
// production factory and wires it with executionEnabled=false, then drives the two
// money-path methods with fully valid inputs (so boundary validation passes and the
// disabled purchaser / exchange runner is actually reached). Both must return
// ErrLadderExecutionDisabled.
//
// It also proves NO AWS call happens: the test runs with no credentials and an
// empty/default AWS config. If the disabled shims delegated to a real SDK client,
// PurchaseLayer / ReshapeBuffer would surface a credentials/network error instead
// of the kill-switch sentinel. Getting ErrLadderExecutionDisabled is proof the
// refusal short-circuits before any outbound call.
func TestWireLadderWriteSide_ExecutionDisabled_RealAWSLadder_RefusesWithoutAWSCall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const (
		region    = "us-east-1"
		accountID = "123456789012"
	)

	// Real AWSLadder from the production factory (read side wired to real clients;
	// construction is offline: the AWS SDK builds clients lazily, no network call).
	capRaw, err := awsladder.NewFromAWSConfig(ctx, region, accountID)
	require.NoError(t, err)

	// Wire the write side with the kill-switch OFF. app fields are unused on the
	// disabled path (WireWriteSideDisabled ignores the exchange adapter), so a
	// zero-value Application is sufficient.
	app := &Application{}
	wired, err := app.wireLadderWriteSide(ctx, false /* executionEnabled */, region, accountID, capRaw)
	require.NoError(t, err)
	require.NotNil(t, wired)

	// --- PurchaseLayer: valid RI recommendation, kill-switch must refuse. ---
	rec := pkgcommon.Recommendation{
		ResourceType:  "m5.large",
		Count:         2,
		Term:          "1yr",
		PaymentOption: "no-upfront",
		Details: &pkgcommon.ComputeDetails{
			InstanceType: "m5.large",
			Platform:     "linux",
			Tenancy:      "default",
			Scope:        "regional",
		},
	}
	opts := pkgcommon.PurchaseOptions{
		Source:           pkgcommon.PurchaseSourceWeb,
		IdempotencyToken: "ladder-tok-1",
	}
	result, purchaseErr := wired.PurchaseLayer(ctx, pkgladder.LayerConvertibleRI, rec, opts)
	require.Error(t, purchaseErr)
	assert.ErrorIs(t, purchaseErr, awsladder.ErrLadderExecutionDisabled,
		"kill-switch OFF must refuse PurchaseLayer with ErrLadderExecutionDisabled, not an AWS error")
	assert.False(t, result.Success, "no purchase may be reported as successful when the kill-switch is off")

	// --- ReshapeBuffer: valid scope + config, kill-switch must refuse. ---
	scope := pkgladder.Scope{Provider: pkgcommon.ProviderAWS, AccountID: accountID}
	reshapeCfg := pkgladder.BufferReshapeConfig{
		MaxPaymentPerExchangeUSD: f64ptr(100.0),
		MaxPaymentDailyUSD:       f64ptr(500.0),
		UtilizationThresholdPct:  20.0,
		LookbackDays:             30,
	}
	_, reshapeErr := wired.ReshapeBuffer(ctx, scope, reshapeCfg)
	require.Error(t, reshapeErr)
	assert.ErrorIs(t, reshapeErr, awsladder.ErrLadderExecutionDisabled,
		"kill-switch OFF must refuse ReshapeBuffer with ErrLadderExecutionDisabled, not an AWS error")
}

// TestPlanningCapability_RealAWSLadder_RefusesPurchases pins that ladder_run plans
// with a capability whose write side is always the disabled one. Planning must
// never hold a purchase-capable capability, whatever ladder_execution_enabled
// says; only the reshape sweep may wire real clients (#70 S3).
func TestPlanningCapability_RealAWSLadder_RefusesPurchases(t *testing.T) {
	// No credentials can be found, so a regression that wired real clients
	// fails on a credentials error instead of buying with the caller's account.
	// t.Setenv rules out t.Parallel.
	for k, v := range map[string]string{
		"AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_PROFILE": "", "AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": "",
		"AWS_SESSION_TOKEN": "", "AWS_EC2_METADATA_DISABLED": "true",
		"AWS_WEB_IDENTITY_TOKEN_FILE": "", "AWS_ROLE_ARN": "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "", "AWS_CONTAINER_CREDENTIALS_FULL_URI": "",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN": "",
	} {
		t.Setenv(k, v)
	}
	ctx := context.Background()
	const (
		region    = "us-east-1"
		accountID = "123456789012"
	)
	app := &Application{LadderCapabilityFactory: func(ctx context.Context, region, accountID string) (pkgladder.LadderCapability, error) {
		return awsladder.NewFromAWSConfig(ctx, region, accountID)
	}}
	capability, err := app.planningCapability(ctx, region, accountID)
	require.NoError(t, err)

	rec := pkgcommon.Recommendation{
		ResourceType:  "m5.large",
		Count:         1,
		Term:          "1yr",
		PaymentOption: "no-upfront",
		Details:       &pkgcommon.ComputeDetails{InstanceType: "m5.large", Platform: "linux", Tenancy: "default", Scope: "regional"},
	}
	_, purchaseErr := capability.PurchaseLayer(ctx, pkgladder.LayerConvertibleRI, rec, pkgcommon.PurchaseOptions{Source: pkgcommon.PurchaseSourceWeb, IdempotencyToken: "ladder-plan-tok"})
	assert.ErrorIs(t, purchaseErr, awsladder.ErrLadderExecutionDisabled, "the planning capability must refuse every purchase")
}
