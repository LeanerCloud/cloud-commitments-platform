package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issue #561: transient STS AssumeRole failures (throttling, 5xx, network
// errors, timeouts) must not be reported as the fixed 400 that tells the
// caller to fix the role; only permanent failures keep that 400.

const (
	marketplaceAssumeRoleThrottleXML = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>Throttling</Code><Message>Rate exceeded for ` + marketplaceTenantRoleARN + `</Message></Error><RequestId>r</RequestId></ErrorResponse>`
	marketplaceAssumeRoleServerXML   = `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Receiver</Type><Code>InternalFailure</Code><Message>internal error assuming ` + marketplaceTenantRoleARN + `</Message></Error><RequestId>r</RequestId></ErrorResponse>`
)

// failingAssumeRoleSTS fails every AssumeRole call at the transport level and
// answers every other STS call as the host account.
type failingAssumeRoleSTS struct{ err error }

func (f failingAssumeRoleSTS) Do(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	if strings.Contains(string(b), "Action=AssumeRole") {
		return nil, f.err
	}
	return hostSTSTransport{}.Do(r)
}

// noRetry keeps the mocked SDK from backing off between attempts.
func noRetry() aws.Retryer { return aws.NopRetryer{} }

func activeListingRow() *config.PurchaseHistoryRecord {
	row := standardRow()
	row.ListingState = config.ListingStateActive
	row.ListingID = "ril-cancel"
	return row
}

func assertNoRoleLeak(t *testing.T, ce *clientError) {
	t.Helper()
	assert.NotEqual(t, marketplaceRoleAssumptionMessage, ce.message)
	assert.NotContains(t, ce.message, marketplaceHostAccount)
	assert.NotContains(t, ce.message, "222222222222")
	assert.NotContains(t, ce.message, "arn:aws:")
	assert.NotContains(t, ce.message, "Rate exceeded")
	assert.NotContains(t, ce.message, "internal error")
}

func TestMarketplaceCancel_TransientAssumeRoleFailureIsRetryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		client     aws.HTTPClient
		wantCode   int
		retryAfter bool
	}{
		{name: "throttling", client: assumeRoleSTS{status: 400, body: marketplaceAssumeRoleThrottleXML}, wantCode: 503, retryAfter: true},
		{name: "sts 5xx", client: assumeRoleSTS{status: 500, body: marketplaceAssumeRoleServerXML}, wantCode: 502, retryAfter: false},
		{name: "sts 503 empty body", client: assumeRoleSTS{status: 503, body: ""}, wantCode: 502, retryAfter: false},
		{name: "network error", client: failingAssumeRoleSTS{errors.New("dial tcp: connection refused")}, wantCode: 502, retryAfter: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := realEC2Handler(t, activeListingRow(), assumeRoleSTS{})
			h.awsCfg.HTTPClient = tc.client
			h.awsCfg.Retryer = noRetry

			_, err := h.marketplaceCancel(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

			ce, ok := IsClientError(err)
			require.True(t, ok, "expected a client error, got %v", err)
			assert.Equal(t, tc.wantCode, ce.code)
			assertNoRoleLeak(t, ce)
			if tc.retryAfter {
				assert.Equal(t, "5", ce.headers["Retry-After"])
			}
		})
	}
}

func TestMarketplaceList_ThrottledAssumeRoleIsRetryable(t *testing.T) {
	t.Parallel()
	h := realEC2Handler(t, standardRow(), assumeRoleSTS{status: 400, body: marketplaceAssumeRoleThrottleXML})
	h.awsCfg.Retryer = noRetry

	_, err := h.marketplaceList(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	ce, ok := IsClientError(err)
	require.True(t, ok, "expected a client error, got %v", err)
	assert.Equal(t, 503, ce.code)
	assert.Equal(t, "5", ce.headers["Retry-After"])
	assertNoRoleLeak(t, ce)
}

// The access-denied class must keep the fixed 400 with retries disabled too.
func TestMarketplaceCancel_DeniedAssumeRoleKeeps400(t *testing.T) {
	t.Parallel()
	h := realEC2Handler(t, activeListingRow(), assumeRoleSTS{status: 403, body: marketplaceAssumeRoleDenyXML})
	h.awsCfg.Retryer = noRetry

	_, err := h.marketplaceCancel(context.Background(), marketplaceReq(), validMarketplacePurchaseID)

	assertTenantSafeRoleDenial(t, err)
}

func stsAssumeRoleErr(inner error) error {
	opErr := &smithy.OperationError{ServiceID: "STS", OperationName: "AssumeRole", Err: inner}
	return &smithy.OperationError{
		ServiceID:     "EC2",
		OperationName: "CancelReservedInstancesListing",
		Err:           fmt.Errorf("failed to refresh cached credentials, %w", opErr),
	}
}

func TestMapAWSMarketplaceError_AssumeRoleClasses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		inner    error
		wantCode int
	}{
		{name: "access denied", inner: &smithy.GenericAPIError{Code: "AccessDenied", Message: "no " + marketplaceTenantRoleARN, Fault: smithy.FaultClient}, wantCode: 400},
		{name: "region disabled", inner: &smithy.GenericAPIError{Code: "RegionDisabledException", Message: "sts disabled", Fault: smithy.FaultClient}, wantCode: 400},
		{name: "unclassified error", inner: errors.New("something odd about " + marketplaceTenantRoleARN), wantCode: 400},
		{name: "throttling", inner: &smithy.GenericAPIError{Code: "Throttling", Message: "Rate exceeded", Fault: smithy.FaultClient}, wantCode: 503},
		{name: "request limit", inner: &smithy.GenericAPIError{Code: "RequestLimitExceeded", Message: "Rate exceeded"}, wantCode: 503},
		{name: "server fault", inner: &smithy.GenericAPIError{Code: "InternalFailure", Message: "internal error", Fault: smithy.FaultServer}, wantCode: 502},
		{name: "request timeout code", inner: &smithy.GenericAPIError{Code: "RequestTimeout", Message: "internal error"}, wantCode: 502},
		{name: "deadline exceeded", inner: fmt.Errorf("request canceled, %w", context.DeadlineExceeded), wantCode: 502},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ce, ok := IsClientError(mapAWSMarketplaceError("AWS cancel listing failed", stsAssumeRoleErr(tc.inner)))

			require.True(t, ok)
			assert.Equal(t, tc.wantCode, ce.code)
			assert.NotContains(t, ce.message, "arn:aws:")
			if tc.wantCode == 400 {
				assert.Equal(t, marketplaceRoleAssumptionMessage, ce.message)
				return
			}
			assertNoRoleLeak(t, ce)
		})
	}
}
