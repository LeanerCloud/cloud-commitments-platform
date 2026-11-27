package archera

import "github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"

// ProjectContract projects the published comparison and line items into the identities MatchRecommendation joins.
func ProjectContract(c *insurance.Comparison, items []insurance.LineItem) (rows []ComparisonIdentity, identities []LineItemIdentity, err error) {
	return insurance.ProjectContract(c, items)
}
