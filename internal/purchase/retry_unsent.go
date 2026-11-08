package purchase

import "github.com/LeanerCloud/cloud-commitments-platform/internal/config"

// RetryRefusalReason is RedriveRefusalReason for the user-facing Retry of a
// failed row. A row proven to have sent nothing (no_submission at attempt 0)
// cannot have bought anything, so the no-duplicate-guard refusals do not apply.
func RetryRefusalReason(exec *config.PurchaseExecution) string {
	return retryRefusalReason(exec, NeverSubmitted(exec) && exec.Status == config.StatusFailed)
}

// retryRefusalReason lifts only the AWS EC2 and Azure savings-plans
// reasons when lineageUnsent. Unknown providers and any other reason still
// refuse: no proof of an unsent lineage says anything about them.
func retryRefusalReason(exec *config.PurchaseExecution, lineageUnsent bool) string {
	for i := range exec.Recommendations {
		reason := recRedriveRefusalReason(exec.Recommendations[i])
		if reason == "" {
			continue
		}
		if lineageUnsent && (reason == redriveReasonAWSNoGuard || reason == redriveReasonAzureSavingsPlan) {
			continue
		}
		return reason
	}
	return ""
}
