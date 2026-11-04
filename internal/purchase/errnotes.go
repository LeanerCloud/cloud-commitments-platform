package purchase

import (
	"errors"
	"regexp"
	"strings"
)

// serverNotePatterns lists every server-authored string stored into
// purchase_executions.error or a recommendation's error (enumerated from the
// assignments in purchase/execution.go, manager.go, reaper.go, recover_reaped.go,
// notifications.go, plan_step_attach.go, stranded_redrive.go, step_cap.go,
// due_age_cap.go, claim.go, ladder_execution_recheck.go and the api retry
// handler's email reasons). Anything else in a stored error is treated as
// provider text. Each pattern is anchored; free tails stop at the next "; ".
// The em dash in the reaper note is written as \x{2014}.
var serverNotePatterns = []string{
	`history_write_failed: commitment [^;\s]+ purchased but its history record failed to save`,
	`recovered after reap; provider purchase succeeded`,
	`ramp not advanced: [^;]*`,
	`reaped after \d+m in [a-z_]+ state \x{2014} executor did not complete(?:; safe to retry)?`,
	`execution was approved but its purchase run was interrupted before completing and never finalized; failed by the recovery sweep so it is not silently stuck \(issue #632\)\. (?:Verify on the cloud provider that no commitment was created, then Retry\.|Retry is refused for this purchase because a repeat could buy a second commitment\. Verify on the cloud provider whether the commitment was created; if it was not, start a new purchase\.)`,
	`refusing to execute retry attempt \d+: (?:` + redriveReasonPattern + `)`,
	`stale Azure recommendation pricing; refresh recommendations and submit a new purchase(?:: [^;]*)?`,
	`(?:execution [^:\s]+: )?plan step has no recommendations attached, so it cannot buy anything \(platform#631: plan steps cannot yet carry recommendations\)`,
	`plan step refused by commitment cap(?:: [^;]*)?`,
	`scheduled purchase skipped: it was due too long ago(?:: it was due at [^;]*\(SCHEDULED_PURCHASE_MAX_OVERDUE\); nothing was submitted, retry it if the purchase is still wanted)?`,
	`purchase refused after claim: the claimed execution no longer passes the authorization checks(?:: [^;]*)?`,
	`refusing to execute ladder purchase: [^;]*`,
	`refusing to execute: ladder tranche lookup failed: [^;]*`,
	`scheduled plan step reached its scheduled date without recommendations; no purchase was submitted`,
	`no current recommendation matched plan [^;\s]+ step \d+ by [^;]* \(\d+-day grace after the plan date\)(?:; ` + skippedNotePattern + `)?`,
	`target already met: target \d+, committed \d+(?:; ` + skippedNotePattern + `)?; \d+ failed row\(s\) of unknown outcome counted as committed`,
	`selected recommendations do not resolve to a single cloud account: (?:\d+ selected recommendation\(s\) target \d+ cloud accounts \([^;]*\); submit one purchase per account|\d+ selected recommendation\(s\) carry no cloud_account_id while \d+ target account [^;]*)`,
	`purchase was not successful`,
	`EC2 recommendation for \S+ has zonal scope but no AvailabilityZone: refusing to buy a zonal RI in an arbitrary AZ; re-fetch the recommendation`,
	`zonal RI offering needs an availability zone; re-fetch the recommendation`,
	`email notifier not configured for this deployment`,
	`no notification email set in Settings \x{2192} General(?: and no account contact emails configured)?`,
	`FROM_EMAIL not configured for this deployment`,
}

const skippedNotePattern = `\d+ recommendation\(s\) without a frozen baseline skipped \(under-buy\): [^;]*`

var redriveReasonPattern = regexp.QuoteMeta(redriveReasonAWSNoGuard) + `|` + regexp.QuoteMeta(redriveReasonAzureSavingsPlan) +
	`|provider "[^"]*" is not known to reject a duplicate purchase, so re-driving this could buy a second commitment`

// A rec's "<resourceType>: " head, as the writer's per-rec join adds it.
var serverNoteRE = regexp.MustCompile(`^(?:[A-Za-z0-9][A-Za-z0-9._-]*: )?(?:` + strings.Join(serverNotePatterns, "|") + `)(?:; |$)`)

// onlyServerNotes reports whether every "; "-separated item of s is an
// allowlisted server note. Matching consumes whole notes, so a "; " inside a
// provider message can never smuggle a tail through.
func onlyServerNotes(s string) bool {
	for s != "" {
		m := serverNoteRE.FindString(s)
		if m == "" {
			return false
		}
		s = s[len(m):]
	}
	return true
}

// ServerNoteMessage returns err's text when it is a known server-authored
// refusal (a sentinel or an allowlisted note), so live responses can show it.
func ServerNoteMessage(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	for _, sentinel := range []error{ErrPlanStepNoRecommendations, ErrStaleAzurePricing, ErrStepCommitmentCap,
		ErrDueRowTooOld, ErrClaimedRowRefused, errAmbiguousAccountScope} {
		if errors.Is(err, sentinel) {
			return err.Error(), true
		}
	}
	if onlyServerNotes(err.Error()) {
		return err.Error(), true
	}
	return "", false
}
