package purchase

import (
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// historyOfferingClass is the class to record for an AWS EC2 purchase. An
// adopted re-drive reports the class of the RI it found, which can differ from
// the configured one if the operator changed it between attempts; an adopted
// result that reports none records "" (NULL), which the marketplace-sell path
// re-reads from AWS, rather than an unproven configured class.
func historyOfferingClass(result common.PurchaseResult, configured ec2types.OfferingClassType) string {
	reported := strings.ToLower(strings.TrimSpace(result.OfferingClass))
	switch reported {
	case "":
		if result.ExistingCommitment {
			return ""
		}
		return string(configured)
	case string(ec2types.OfferingClassTypeStandard), string(ec2types.OfferingClassTypeConvertible):
		if !strings.EqualFold(reported, string(configured)) {
			logging.Warnf("purchase history: commitment %s reports offering class %q, configured class is %q; recording the reported class",
				result.CommitmentID, reported, configured)
		}
		return reported
	default:
		logging.Warnf("purchase history: commitment %s reports unknown offering class %q; recording it as unknown",
			result.CommitmentID, result.OfferingClass)
		return ""
	}
}

// historyUpfrontCost is the upfront cost to record. A Compute Engine CUD has no
// upfront charge, so an adopted one (the provider reports no Cost) is a known
// zero; recording nil would null every upfront aggregate for the term. AWS and
// Azure adoptions can hide a real upfront payment, so they stay nil (unknown),
// as does a fresh GCP result with no Cost.
func historyUpfrontCost(provider string, service common.ServiceType, result common.PurchaseResult) *float64 {
	if result.Cost == nil && result.ExistingCommitment && isGCPCompute(provider, service) {
		zero := 0.0
		return &zero
	}
	return result.Cost
}
