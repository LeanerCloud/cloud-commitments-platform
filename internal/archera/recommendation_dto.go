package archera

import "github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"

type (
	RecommendationComparisonDTO  = insurance.RecommendationComparisonDTO
	RecommendationOfferDTO       = insurance.RecommendationOfferDTO
	RecommendationAlternativeDTO = insurance.RecommendationAlternativeDTO
)

// BuildRecommendationComparison matches the stored recommendation against the vendor line items
// and renders only the matched row. An unmatched result carries no figures. It returns
// ErrUnrepresentable when a rendered number has no exact decimal form.
func BuildRecommendationComparison(id, planID string, rec *RecommendationIdentity, c *insurance.Comparison, items []insurance.LineItem) (*RecommendationComparisonDTO, error) {
	return insurance.BuildRecommendationComparison(id, planID, rec, c, items)
}
