package purchase

import "strings"

// persistentFailureHints maps known-persistent failure substrings to
// the operator-actionable hint surfaced on the History row in place of
// the Retry button (issue #47, Q3). The match is a case-INsensitive
// substring contains check (see ResolveOpsHint), wording variations
// like "ses sandbox" / "SES Sandbox" all hit. Keep needles short and
// distinctive so legitimate transient failures don't accidentally
// substring-match a persistent hint and lock the user out.
//
// Membership criteria: only failures that NO retry can possibly fix:
// they require an operator to change configuration or unblock an
// upstream resource. SES sandbox / unverified domain / missing
// FROM_EMAIL all fit; a transient SES throttle does NOT (the next
// retry might succeed). When in doubt, leave it out, false-negatives
// cost a wasted retry; false-positives lock the user out of an
// actionable button entirely.
var persistentFailureHints = map[string]string{
	"FROM_EMAIL not configured": "Set FROM_EMAIL tfvar then retry",
	"SES sandbox":               "Move SES out of sandbox or verify recipient, then retry",
	"SES domain not verified":   "Verify SES domain in AWS console, then retry",
	"IAM denied":                "Grant the deploy role missing IAM permission, then retry",
}

// ResolveOpsHint returns a non-empty hint when the failure reason
// matches a known-persistent pattern, else empty. Match is case-
// insensitive substring contains so backend wording variations don't
// silently slip through ("ses sandbox" / "SES sandbox" / "SES Sandbox"
// all hit).
func ResolveOpsHint(failureReason string) string {
	if failureReason == "" {
		return ""
	}
	lower := strings.ToLower(failureReason)
	for needle, hint := range persistentFailureHints {
		if strings.Contains(lower, strings.ToLower(needle)) {
			return hint
		}
	}
	return ""
}
