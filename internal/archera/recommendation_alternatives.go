package archera

import "math/big"

type CandidateIdentity struct {
	SelectedAmount                   *big.Rat
	OfferID, Provider, Type, Payment string
}

// CandidateStatus classifies a vendor candidate against the stored recommendation.
type CandidateStatus string

const (
	CandidateVendorAlternative CandidateStatus = "vendor_alternative"
	CandidateUnavailable       CandidateStatus = "unavailable"
)

type CandidateAssociation struct {
	OfferID              string
	Status               CandidateStatus
	Reason               Reason
	NativeIdentityStatus CandidateStatus
}

func associateCandidate(r *RecommendationIdentity, c *CandidateIdentity) CandidateAssociation {
	out := CandidateAssociation{OfferID: c.OfferID, Status: CandidateUnavailable, Reason: ReasonCandidateBasisUnproven, NativeIdentityStatus: CandidateUnavailable}
	if validMatchUUID(c.OfferID) && c.Provider == r.Provider && c.Type == commitmentSavingsPlan && validPayment(c.Payment) && sameAmount(r.HourlyCommitment, c.SelectedAmount) {
		out.Status, out.Reason = CandidateVendorAlternative, ""
	}
	return out
}
