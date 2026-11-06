package purchase

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// redshiftRetrySettle is how long after a failed Redshift row's last write a
// Retry waits, so a purchase AWS accepted late is visible to the dispatch dedupe.
const redshiftRetrySettle = 10 * time.Minute

// Fixed id-free texts. A stamp-less row is not necessarily old (a reaped
// never-sent row has none either), so no text says so.
const (
	redshiftRetryNoStampRefusal = "this Redshift purchase has no first dispatch time recorded, so a retry could buy a second reservation; check the account in AWS and start a new purchase"
	redshiftRetryHorizonRefusal = "this Redshift purchase was first dispatched at least 24h ago, so a retry could buy a second reservation; check the account in AWS and start a new purchase"
)

// IsRedshiftExecution reports whether exec has a selected Redshift recommendation.
func IsRedshiftExecution(exec *config.PurchaseExecution) bool {
	return hasRedshiftRec(exec)
}

// NeverSubmitted reports a first-attempt row proven to have sent nothing.
func NeverSubmitted(exec *config.PurchaseExecution) bool {
	return exec.NoSubmission && exec.RetryAttemptN == 0
}

// RedshiftRetryPermanentRefusal returns the reason a failed Redshift execution
// can never be retried, or "". sinceStamp is the age of its first dispatch on
// the database clock, nil when the row has no stamp.
func RedshiftRetryPermanentRefusal(exec *config.PurchaseExecution, sinceStamp *time.Duration) string {
	if !hasRedshiftRec(exec) {
		return ""
	}
	if NeverSubmitted(exec) {
		return "" // its successor mints a fresh stamp, so the old one is irrelevant
	}
	if sinceStamp == nil {
		return redshiftRetryNoStampRefusal
	}
	if *sinceStamp >= redshiftRedriveHorizon {
		return redshiftRetryHorizonRefusal
	}
	return ""
}

// RedshiftRetryWait returns how much longer a Retry must wait for sinceUpdate
// (age of the row's last write on the database clock) to reach the settle time.
func RedshiftRetryWait(exec *config.PurchaseExecution, sinceUpdate time.Duration) time.Duration {
	if !hasRedshiftRec(exec) || NeverSubmitted(exec) || sinceUpdate >= redshiftRetrySettle {
		return 0
	}
	return redshiftRetrySettle - sinceUpdate
}
