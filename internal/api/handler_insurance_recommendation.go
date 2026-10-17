package api

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/aws/aws-lambda-go/events"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/archera"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

const (
	recommendationQueryKey   = "recommendation_id"
	maxRecommendationIDBytes = 1024
)

var recommendationQuerySplit = regexp.MustCompile(`[&;]`)

// rawRecommendationKeyCount counts raw query occurrences of the key after unescaping, so an encoded or
// ";"-separated spelling cannot slip past the map the adapters build.
func rawRecommendationKeyCount(raw string) int {
	n := 0
	if raw == "" {
		return 0
	}
	for _, part := range recommendationQuerySplit.Split(raw, -1) {
		key, _, _ := strings.Cut(part, "=")
		if decoded, err := url.QueryUnescape(key); err == nil && decoded == recommendationQueryKey {
			n++
		}
	}
	return n
}

func insuranceRecommendationRequested(req *events.LambdaFunctionURLRequest) bool {
	_, ok := req.QueryStringParameters[recommendationQueryKey]
	return ok || rawRecommendationKeyCount(req.RawQueryString) > 0
}

func insuranceRecommendationID(req *events.LambdaFunctionURLRequest) (string, error) {
	id := req.QueryStringParameters[recommendationQueryKey]
	if n := rawRecommendationKeyCount(req.RawQueryString); n > 1 {
		return "", NewClientError(400, "invalid recommendation query")
	}
	if id == "" || len(id) > maxRecommendationIDBytes {
		return "", NewClientError(400, "invalid recommendation identifier")
	}
	return id, nil
}

// getInsuranceRecommendation compares one stored recommendation with its Archera line item.
// The auth gate and the exact-one store read come before any secret or vendor work.
func (h *Handler) getInsuranceRecommendation(ctx context.Context, req *events.LambdaFunctionURLRequest) (*archera.RecommendationComparisonDTO, error) {
	if err := h.requireInsuranceAccess(ctx, req); err != nil {
		return nil, err
	}
	id, err := insuranceRecommendationID(req)
	if err != nil {
		return nil, err
	}
	record, err := h.storedRecommendation(ctx, id)
	if err != nil {
		return nil, err
	}
	rec, err := archera.ProjectRecommendation(record)
	if err != nil {
		return nil, NewClientError(422, "stored recommendation is not comparable")
	}
	c, items, err := h.FetchRecommendationComparison(ctx, rec)
	if err != nil {
		return nil, err
	}
	if !h.recommendationUnchanged(ctx, id, rec) {
		return nil, errRecommendationChanged
	}
	dto, err := archera.BuildRecommendationComparison(id, h.insurance.PlanID(), rec, c, items)
	if err != nil {
		return nil, NewClientError(502, "Archera recommendation comparison is unavailable")
	}
	raw, err := json.Marshal(dto)
	if err != nil || len(raw) > maxComparisonResponseBytes {
		return nil, NewClientError(502, "comparison too large to return")
	}
	return dto, nil
}

// recommendationUnchanged rereads on the caller's context; any change, deletion or store error is a change.
func (h *Handler) recommendationUnchanged(ctx context.Context, id string, rec *archera.RecommendationIdentity) bool {
	again, err := h.storedRecommendation(ctx, id)
	if err != nil {
		return false
	}
	now, err := archera.ProjectRecommendation(again)
	return err == nil && rec.Equal(now)
}

var errRecommendationChanged = NewClientError(409, "recommendation changed during comparison")

func (h *Handler) storedRecommendation(ctx context.Context, id string) (*config.RecommendationRecord, error) {
	if h.config == nil {
		return nil, NewClientError(503, "recommendation store is unavailable")
	}
	rows, err := h.config.ListStoredRecommendations(ctx, config.RecommendationFilter{ID: id})
	switch {
	case err != nil:
		return nil, NewClientError(503, "recommendation store is unavailable")
	case len(rows) == 0:
		return nil, errNotFound
	case len(rows) > 1:
		return nil, NewClientError(409, "recommendation identity is ambiguous")
	}
	return &rows[0], nil
}
