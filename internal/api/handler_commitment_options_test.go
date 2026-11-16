package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubCommitmentOpts lets handler tests drive the endpoint without the full
// probe+store machinery. Mirrors the CommitmentOptsInterface surface.
type stubCommitmentOpts struct {
	getFn      func(ctx context.Context) (commitmentopts.Options, error)
	validateFn func(ctx context.Context, provider, service string, term int, payment string) (bool, error)
	cachedFn   func(ctx context.Context, provider string) (map[string][]commitmentopts.Combo, bool, error)
}

func (s *stubCommitmentOpts) CachedOptions(ctx context.Context, provider string) (map[string][]commitmentopts.Combo, bool, error) {
	if s.cachedFn == nil {
		return nil, false, nil
	}
	return s.cachedFn(ctx, provider)
}

func (s *stubCommitmentOpts) Get(ctx context.Context) (commitmentopts.Options, error) {
	return s.getFn(ctx)
}

func (s *stubCommitmentOpts) Validate(ctx context.Context, provider, service string, term int, payment string) (bool, error) {
	return s.validateFn(ctx, provider, service, term, payment)
}

func TestGetCommitmentOptions_NilService_ReturnsUnavailable(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: nil}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.AWS)
}

func TestGetCommitmentOptions_ErrNoData_ReturnsUnavailable(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) {
			return nil, commitmentopts.ErrNoData
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.AWS)
}

func TestGetCommitmentOptions_UnexpectedError_CollapsesToUnavailable(t *testing.T) {
	t.Parallel()
	// Any non-ErrNoData failure (DB blip, ctx cancellation, etc.) must
	// still return 200 + unavailable so the Settings page overlay doesn't
	// break on transient server issues. The error is logged, not returned.
	boom := errors.New("database exploded")
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) {
			return nil, boom
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.AWS)
}

func TestNewHandler_CommitmentOptsWired(t *testing.T) {
	t.Parallel()
	// Regression guard: silently dropping the HandlerConfig → Handler wire
	// (via a field rename or accidental removal) would re-introduce the
	// "endpoint always returns unavailable" bug we already fixed once.
	stub := &stubCommitmentOpts{}
	h := NewHandler(HandlerConfig{CommitmentOpts: stub})
	require.Same(t, stub, h.commitmentOpts)
}

func TestGetCommitmentOptions_EmptyAWS_CollapsesToUnavailable(t *testing.T) {
	t.Parallel()
	// Probe succeeded but returned nothing for AWS. Treating this as
	// unavailable keeps the frontend on its hardcoded fallback rather than
	// rendering an empty constraint set.
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) {
			return commitmentopts.Options{"aws": map[string][]commitmentopts.Combo{}}, nil
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.AWS)
}

func TestGetCommitmentOptions_Success_ReturnsAWSCombos(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) {
			return commitmentopts.Options{
				"aws": map[string][]commitmentopts.Combo{
					"rds": {
						{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"},
						{Provider: "aws", Service: "rds", TermYears: 1, Payment: "partial-upfront"},
						{Provider: "aws", Service: "rds", TermYears: 3, Payment: "all-upfront"},
					},
					"elasticache": {
						{Provider: "aws", Service: "elasticache", TermYears: 1, Payment: "no-upfront"},
						{Provider: "aws", Service: "elasticache", TermYears: 3, Payment: "no-upfront"},
					},
				},
			}, nil
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Status)
	assert.Len(t, resp.AWS["rds"], 3)
	assert.Len(t, resp.AWS["elasticache"], 2)
	// Spot-check the payload shape: term/payment only, no provider/service echo.
	assert.Contains(t, resp.AWS["elasticache"], commitmentOptionCombo{Term: 3, Payment: "no-upfront"})
}

// azureSvc is the service key the real Azure probe emits, taken from the
// prober so the fixtures cannot drift from production.
var azureSvc = (&commitmentopts.AzureSPProber{}).Service()

func azureCombos() map[string][]commitmentopts.Combo {
	return map[string][]commitmentopts.Combo{
		azureSvc: {
			{Provider: "azure", Service: azureSvc, TermYears: 1, Payment: "upfront"},
			{Provider: "azure", Service: azureSvc, TermYears: 3, Payment: "monthly"},
		},
	}
}

func awsRDSOptions() commitmentopts.Options {
	return commitmentopts.Options{"aws": {"rds": {{Provider: "aws", Service: "rds", TermYears: 1, Payment: "all-upfront"}}}}
}

func azureOnlyCached(t *testing.T) func(context.Context, string) (map[string][]commitmentopts.Combo, bool, error) {
	return func(_ context.Context, provider string) (map[string][]commitmentopts.Combo, bool, error) {
		require.Equal(t, "azure", provider, "the Azure section must read the azure provider only")
		return azureCombos(), true, nil
	}
}

func TestGetCommitmentOptions_AzureOnly_NoAWSData_ReturnsOK(t *testing.T) {
	t.Parallel()
	// An Azure-only deployment: Get reports ErrNoData (no AWS account) but the
	// azure probe row is cached. The Azure rows must not be hidden.
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn:    func(context.Context) (commitmentopts.Options, error) { return nil, commitmentopts.ErrNoData },
		cachedFn: azureOnlyCached(t),
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Status)
	assert.Nil(t, resp.AWS)
	assert.Equal(t, []commitmentOptionCombo{{Term: 1, Payment: "upfront"}, {Term: 3, Payment: "monthly"}}, resp.Azure[azureSvc])
}

func TestGetCommitmentOptions_AWSAndAzure_BothReturned(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) {
			// Get may also carry azure rows; the Azure section comes from CachedOptions.
			o := awsRDSOptions()
			o["azure"] = map[string][]commitmentopts.Combo{"ignored": {{TermYears: 5, Payment: "x"}}}
			return o, nil
		},
		cachedFn: azureOnlyCached(t),
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Status)
	assert.Len(t, resp.AWS["rds"], 1)
	assert.Len(t, resp.Azure[azureSvc], 2)
	assert.NotContains(t, resp.Azure, "ignored")
}

func TestGetCommitmentOptions_NoAzureRow_OmitsAzureKeyAndKeepsAWSBytes(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) { return awsRDSOptions(), nil },
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	body, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.JSONEq(t, `{"aws":{"rds":[{"payment":"all-upfront","term":1}]},"status":"ok"}`, string(body))
	assert.NotContains(t, string(body), "azure")
}

func TestGetCommitmentOptions_EmptyAzureRow_IsOmittedAndUnavailableWithoutAWS(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) { return nil, commitmentopts.ErrNoData },
		cachedFn: func(context.Context, string) (map[string][]commitmentopts.Combo, bool, error) {
			return map[string][]commitmentopts.Combo{}, true, nil
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.Azure)
}

func TestGetCommitmentOptions_AzureReadError_KeepsAWS(t *testing.T) {
	t.Parallel()
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) { return awsRDSOptions(), nil },
		cachedFn: func(context.Context, string) (map[string][]commitmentopts.Combo, bool, error) {
			return nil, false, errors.New("db blip")
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Status)
	assert.Len(t, resp.AWS["rds"], 1)
	assert.Nil(t, resp.Azure)
}

func TestGetCommitmentOptions_UnexpectedGetError_StaysUnavailableWithoutAzureRead(t *testing.T) {
	t.Parallel()
	called := false
	h := &Handler{commitmentOpts: &stubCommitmentOpts{
		getFn: func(context.Context) (commitmentopts.Options, error) { return nil, errors.New("database exploded") },
		cachedFn: func(context.Context, string) (map[string][]commitmentopts.Combo, bool, error) {
			called = true
			return azureCombos(), true, nil
		},
	}}

	resp, err := h.getCommitmentOptions(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "unavailable", resp.Status)
	assert.Nil(t, resp.Azure)
	assert.False(t, called)
}
