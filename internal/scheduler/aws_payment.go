package scheduler

import (
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// awsRecommendationPayment maps the cross-provider global default payment to
// a value the AWS recommendations client accepts. AWS tokens and "" pass
// through; anything else (Azure/GCP "monthly"/"upfront") becomes "" (the
// Cost Explorer default) with one WARN naming the stored value (#1229).
func awsRecommendationPayment(stored string) string {
	canon, ok := config.NormalizePaymentOption("aws", stored)
	if ok {
		return canon
	}
	logging.Warnf("global default_payment %q is not an AWS payment option; using the Cost Explorer default for AWS recommendations", stored)
	return ""
}
