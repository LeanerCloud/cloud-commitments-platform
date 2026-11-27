package archera

import "github.com/LeanerCloud/cloud-commitments-go/pkg/insurance"

const (
	awsSPYearSeconds      int64 = 31536000
	awsSPThreeYearSeconds int64 = 94608000
)

type (
	RecommendationIdentity = insurance.RecommendationIdentity
	LineItemIdentity       = insurance.LineItemIdentity
	ComparisonIdentity     = insurance.ComparisonIdentity
	MatchStatus            = insurance.MatchStatus
	Reason                 = insurance.Reason
	RecommendationMatch    = insurance.RecommendationMatch
	SelectStatus           = insurance.SelectStatus
)

const (
	MatchUnavailable   = insurance.MatchUnavailable
	ResourceAssociated = insurance.ResourceAssociated

	ReasonIdentityUnproven           = insurance.ReasonIdentityUnproven
	ReasonAmbiguousLineItems         = insurance.ReasonAmbiguousLineItems
	ReasonComparisonBasisUnproven    = insurance.ReasonComparisonBasisUnproven
	ReasonCandidateBasisUnproven     = insurance.ReasonCandidateBasisUnproven
	ReasonStoredDecimalOnly          = insurance.ReasonStoredDecimalOnly
	ReasonUnavailableSourcePrecision = insurance.ReasonUnavailableSourcePrecision

	SelectOne       = insurance.SelectOne
	SelectNone      = insurance.SelectNone
	SelectAmbiguous = insurance.SelectAmbiguous
)

func MatchRecommendation(planID string, rec *RecommendationIdentity, rows []ComparisonIdentity, items []LineItemIdentity) (RecommendationMatch, error) {
	return insurance.MatchRecommendation(planID, rec, rows, items)
}

func SelectLineItem(rec *RecommendationIdentity, items []LineItemIdentity) (id string, status SelectStatus) {
	return insurance.SelectLineItem(rec, items)
}
