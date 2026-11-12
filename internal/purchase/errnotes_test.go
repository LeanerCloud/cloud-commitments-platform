package purchase

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
)

// Every server-authored string stored into purchase_executions.error or a
// recommendation's error must come back verbatim.
func TestSanitizeStoredError_AllowlistedNotesPassThroughVerbatim(t *testing.T) {
	fresh := &config.PurchaseExecution{}
	recordHistoryAuditGap(fresh, "c-1", fmt.Errorf("db down"))
	retry := &config.PurchaseExecution{RetryAttemptN: 2, Recommendations: []config.RecommendationRecord{{Provider: "aws", Service: "ec2"}}}
	azSP := &config.PurchaseExecution{RetryAttemptN: 1, Recommendations: []config.RecommendationRecord{{Provider: "azure", Service: "savingsplans"}}}
	gcpRetry := &config.PurchaseExecution{RetryAttemptN: 1, Recommendations: []config.RecommendationRecord{{Provider: "weird"}}}
	armedMgr := &Manager{config: unprovenPredecessorStore(t)}
	notes := []string{
		fresh.Error,
		recoveredAfterReapNote,
		"ramp not advanced",
		"reaped after 30m in running state — executor did not complete",
		"reaped after 30m in approved state — executor did not complete; safe to retry",
		strandedRecoveryError(&config.PurchaseExecution{}),
		strandedRecoveryError(retry),
		armedRefusalText(t, armedMgr, retry),
		"refusing to execute retry attempt 2: " + redriveReasonAWSNoGuardLegacy,
		armedRefusalText(t, armedMgr, azSP),
		armedRefusalText(t, armedMgr, gcpRetry),
		armedRedriveLookupRefusal,
		ErrStaleAzurePricing.Error(),
		ErrPlanStepNoRecommendations.Error(),
		legacyPlanStepNoRecommendationsNote,
		"execution 00000000-0000-0000-0000-000000000000: " + legacyPlanStepNoRecommendationsNote,
		fmt.Errorf("execution %s: %w", "00000000-0000-0000-0000-000000000000", ErrPlanStepNoRecommendations).Error(),
		fmt.Errorf("%w: plan has no max_step_commitment set (auto-purchase of steps with recommendations is refused until one is set)", ErrStepCommitmentCap).Error(),
		fmt.Errorf("%w: step total commitment 10.00 exceeds max_step_commitment 5.00", ErrStepCommitmentCap).Error(),
		dueRowAgeRefusal(&config.PurchaseExecution{ScheduledDate: time.Now().Add(-100 * time.Hour)}, time.Hour, time.Now()).Error(),
		ErrClaimedRowRefused.Error(),
		"refusing to execute ladder purchase: its ladder run was canceled",
		`refusing to execute ladder purchase: its ladder run status "weird_state" is unknown`,
		"refusing to execute ladder purchase: its tranche is paused",
		ladderNoticeNotSentNote,
		ladderNeedsSecondApproverNote,
		ladderRINeedsConvertibleNote,
		"refusing to execute: ladder tranche lookup failed",
		"refusing to execute ladder purchase: global config unreadable",
		"scheduled plan step reached its scheduled date without recommendations; no purchase was submitted",
		"no current recommendation matched plan 00000000-0000-0000-0000-000000000000 step 2 by 2026-01-02 (3-day grace after the plan date)",
		"no current recommendation matched plan 00000000-0000-0000-0000-000000000000 step 2 by 2026-01-02 (3-day grace after the plan date); " + skippedNote([]string{"a\x1fb", "c"}),
		"target already met: target 10, committed 12; 0 failed row(s) of unknown outcome counted as committed",
		"target already met: target 10, committed 12; " + skippedNote([]string{"a"}) + "; 1 failed row(s) of unknown outcome counted as committed",
		fmt.Errorf("%w: 2 selected recommendation(s) target 2 cloud accounts (00000000-0000-0000-0000-00000000000a, 00000000-0000-0000-0000-00000000000b); submit one purchase per account", errAmbiguousAccountScope).Error(),
		fmt.Errorf("%w: 1 selected recommendation(s) carry no cloud_account_id while 2 target account 00000000-0000-0000-0000-00000000000a", errAmbiguousAccountScope).Error(),
		"purchase was not successful",
		skippedAccountRetryNote,
		skippedAccountNoRetryNote,
		redshiftConflictRefusal,
		redshiftHorizonRefusal,
		redshiftNoStampRefusal,
		redshiftFailedConflictRefusal,
		redshiftNoJournalRefusal,
		redshiftRecentBuyRefusal,
		redshiftRecentBuyRefusalLegacy,
		redshiftSettleRefusal,
		redshiftDuplicateTupleRefusal,
		elastiCacheDuplicatePoolRefusal,
		gcpDuplicateCheckFailedNote,
		gcpRecentCommitmentNote,
		elastiCacheDuplicateCheckFailedNote,
		elastiCacheRecentReservationNote,
		"EC2 recommendation for m5.large has zonal scope but no AvailabilityZone: refusing to buy a zonal RI in an arbitrary AZ; re-fetch the recommendation",
		"zonal RI offering needs an availability zone; re-fetch the recommendation",
		"email notifier not configured for this deployment",
		"no notification email set in Settings → General and no account contact emails configured",
		"no notification email set in Settings → General",
		"FROM_EMAIL not configured for this deployment",
		"config load failed",
		"exchange record has no region; cannot execute safely",
		"daily spending cap is not configured (RIExchangeMaxDailyUSD is 0)",
		"per-exchange spending cap is not configured (RIExchangeMaxPerExchangeUSD is 0)",
		"RI exchange daily cap exceeded: spend 900.000000 exceeds cap 800.000000",
		"RI exchange daily cap exceeded: initial 10.000000, remaining 5.000000",
		"daily spending cap check failed",
		`reserved ceiling "x" is not a decimal`,
		"exchange reservation returned no record ID",
		`exchange reservation returned invalid ceiling "x"`,
		`unknown provider "AWS" (valid: aws, azure, gcp)`,
		ErrCategoryMessage(ErrCategoryAccessDenied),
	}
	exchangeNotes := notes[slices.Index(notes, "config load failed"):slices.Index(notes, ErrCategoryMessage(ErrCategoryAccessDenied))]
	for _, n := range notes {
		assert.NotEmpty(t, n)
		assert.Equal(t, n, SanitizeStoredError(n), n)
		if slices.Contains(exchangeNotes, n) {
			continue
		}
		head := "m5.large: "
		assert.Equal(t, n, SanitizeStoredError(partialRunPrefix + head + n)[len(partialRunPrefix)+len(head):], "behind a resource head: "+n)
	}
	joined := notes[0] + "; " + notes[1] + "; " + notes[6]
	assert.Equal(t, joined, SanitizeStoredError(joined))
	assert.Equal(t, partialRunPrefix+joined, SanitizeStoredError(partialRunPrefix+joined))
}

func TestSanitizeStoredError_UnknownTextFailsClosed(t *testing.T) {
	for _, in := range []string{
		"some unlisted text",
		"history_write_failed: commitment c purchased; and then provider text",
		"ramp not advanced: x; some tail",
		"purchase failed: boom",
		"send failed: SES said no",
		"failed to get plan: db",
	} {
		out := SanitizeStoredError(in)
		assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), out, in)
	}
}

// Rows stored before #1708 carry the old text that named an internal issue;
// it must keep passing the allowlist, and the live text must not name one.
const legacyPlanStepNoRecommendationsNote = "plan step has no recommendations attached, so it cannot buy anything (platform#631: plan steps cannot yet carry recommendations)"

func TestErrPlanStepNoRecommendations_NamesNoInternalIssue(t *testing.T) {
	assert.NotContains(t, ErrPlanStepNoRecommendations.Error(), "#")
}

// A recommendation collection failure is stored as one "<provider>: <category
// message>" item per failed provider.
func TestSanitizeStoredError_CollectionCategoryForm(t *testing.T) {
	stored := "aws: " + ErrCategoryMessage(ErrCategoryAccessDenied) + "; azure: " + ErrCategoryMessage(ErrCategoryUnknown)
	assert.Equal(t, stored, SanitizeStoredError(stored))
	assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(stored+"; gcp: raw text for project my-project"))
}

// A variable part outside its closed alphabet (a newline, a provider or DB
// error text) never reaches the output: only the fixed head or nothing shows.
func TestSanitizeStoredError_ExchangeNoteTailsAreClosed(t *testing.T) {
	cases := []struct{ in, want string }{
		{"RI exchange daily cap exceeded: spend 1\nDETAIL pq: boom exceeds cap 2", "RI exchange daily cap exceeded"},
		{"RI exchange daily cap exceeded: initial 1.0, remaining pq: connection refused", "RI exchange daily cap exceeded"},
		{"RI exchange daily cap exceeded: spend x y exceeds cap 2.0", "RI exchange daily cap exceeded"},
		{"reserved ceiling \"a\nb\" is not a decimal", "reserved ceiling"},
		{"reserved ceiling \"pq: boom\" is not a decimal", "reserved ceiling"},
		{"exchange reservation returned invalid ceiling \"x\ny\"", "exchange reservation returned invalid ceiling"},
		{"unknown provider \"evil\nprovider\" (valid: aws, azure, gcp)", "unknown provider"},
		{"unknown provider \"db error: password for x\" (valid: aws, azure, gcp)", "unknown provider"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, SanitizeStoredError(c.in), c.in)
	}
	for _, in := range []string{
		"config load failed\nsecret",
		"config load failed: pq: boom",
		"exchange record has no region; cannot execute safely\nx",
		"daily spending cap check failed: pq: boom",
		"exchange reservation returned no record ID: pq: boom",
		"daily spending cap is not configured (RIExchangeMaxDailyUSD is 0)\nx",
		"aws: " + ErrCategoryMessage(ErrCategoryUnknown) + "\nraw",
	} {
		assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(in), in)
	}
}

// The head ahead of a note must be a real resource type (per-rec notes) or
// aws|azure|gcp (collection notes); an account number, address, host or DB user
// in that slot is never shown.
func TestSanitizeStoredError_HeadsAreClosed(t *testing.T) {
	note := "plan step refused by commitment cap"
	category := ErrCategoryMessage(ErrCategoryUnavailable)
	for _, h := range []string{
		"m5.large", "db.t4g.micro", "cache.t3.micro", "dc2.large", "ra3.xlplus", "ra3.16xlarge",
		"t3.small.search", "Standard_D2_v3", "Basic_A1", "n2-standard-2", "e2-micro", "c3-highcpu-8",
		// Savings Plan types (aws-sdk SavingsPlanType, azure computeResourceType).
		"Compute", "EC2Instance", "SageMaker", "Database",
		"db.serverless", "u-6tb1.metal", "u-6tb1.112xlarge", "mac2-m2pro.metal", "ultrawarm1.medium.search",
		"m7i.metal-24xl", "n2-custom-4-8192", "n2d-custom-2-8192-ext", "e2-custom-4-8192", "e2-custom-medium-2048",
		"a2-ultragpu-8g", "a2-highgpu-1g", "a3-megagpu-8g", "c3d-highmem-360-lssd", "m1-ultramem-40", "h4d-highmem-192",
		"Standard_D2", "Standard_B2ats_v2", "Standard_NV24ads_A10_v5", "Standard_NC6s_v3", "Basic_C0",
		"D2", "D2s_v3", "NC6s_v3", "A1",
		"GP_Gen5_8", "BC_Gen5_16", "GENERAL_PURPOSE", "c7i-flex.large",
		// Real library types that used to fall back (#1753).
		"Premium_P1", "100RU", "Cosmos_DB_100_RUs", "GeneralPurpose_Gen5_2", "db-n1-standard-1",
		"STANDARD", "STANDARD_HA", "BASIC", "u7i-12tb.224xlarge", "a3-edgegpu-8g",
		// One row per closed-set prefix, so none can be dropped silently.
		"DW100c", "DW500c", "oi2.large.search", "om2.xlarge.search", "a1.medium", "c5.large", "d3.xlarge", "dc2.large",
		"cc2.8xlarge", "cg1.4xlarge", "cr1.8xlarge", "dl1.24xlarge", "ds2.xlarge", "f1.2xlarge", "g5.xlarge", "gr6.4xlarge",
		"h1.2xlarge", "hi1.4xlarge", "hs1.8xlarge", "i4i.large", "im4gn.large", "inf2.xlarge", "is4gen.medium", "p4d.24xlarge",
		"r6g.large", "t3.micro", "trn1.2xlarge", "u-6tb1.56xlarge", "vt1.3xlarge", "x2idn.16xlarge", "z1d.large", "or1.large.search",
		"Standard_B2ms", "Standard_DC2ads_v5", "Standard_DS3_v2", "Standard_E2s_v5", "Standard_EC2ads_v5", "Standard_ES1", "Standard_F2s_v2",
		"Standard_FX4mds", "Standard_G2", "Standard_GS1", "Standard_H8", "Standard_HB120rs_v3", "Standard_HC44rs", "Standard_HX176rs",
		"Standard_L8s_v3", "Standard_LS1", "Standard_M128ms", "Standard_ND96isr_H100_v5", "Standard_NV6",
		"B2ms", "DC2ads_v5", "DS3_v2", "E2s_v5", "EC2ads_v5", "ES1", "F2s_v2", "FX4mds", "G2", "GS1", "H8", "HB120rs_v3", "HC44rs", "HX176rs",
		"L8s_v3", "LS1", "M128ms", "ND96isr_v5", "NV6", "NC6s_v3", "NG8ads_V620", "NP10s",
		"db-custom-2-8192", "c3-standard-192-metal", "NP10s", "NG8ads_V620", "Standard_D02", "e2-custom-medium-2048", "hpc7g.16xlarge", "a1.medium", "or1.medium.search", "db.r6g.large", "cache.r7g.large",
	} {
		assert.Equal(t, h+": "+note, SanitizeStoredError(h+": "+note), h)
		assert.Equal(t, h+": "+category, SanitizeStoredError(h+": "+category), h)
	}
	for _, h := range []string{"aws", "azure", "gcp"} {
		assert.Equal(t, h+": "+category, SanitizeStoredError(h+": "+category), h)
	}
	for _, h := range []string{
		"123456789012", "10.20.30.40", "db-prod.internal", "db.internal.example", "cudly_app", "web1.prod.example",
		"m5.large.example.com", "ec2.large.example", "arn:aws:iam::123456789012:role/x", "evil", "AWS",
		// Short made-up tokens that fit the old family shapes (#1753).
		"DB1", "SQL1", "PG01", "web1.medium", "db1.large", "db.app1.large", "n2-standard-db-prod", "e2-micro-admin", "D02", "NX6", "db-n1-standard-db",
	} {
		unknown := ErrCategoryMessage(ErrCategoryUnknown)
		assert.Equal(t, unknown, SanitizeStoredError(h+": "+category), h)
		assert.Equal(t, unknown, SanitizeStoredError(h+": "+note), h)
	}
	for _, in := range []string{
		"123456789012: config load failed",
		"10.20.30.40: config load failed",
		"db-prod.internal: daily spending cap check failed",
		"cudly_app: the requested commitment is not available from the provider",
		"m5.large: config load failed",
		"aws: " + note,
	} {
		out := SanitizeStoredError(in)
		assert.NotContains(t, out, ": ", in)
		assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), out, in)
	}
}

func TestSanitizeStoredError_ResolveNoteExchangeIDIsClosed(t *testing.T) {
	base := `operator:completed: account="` + "1234" + "56789012" + `" region="us-east-1" checked_at=2026-01-02T03:04:05Z`
	ok := base + ` exchange_id="abc-123" reason="done"`
	assert.Equal(t, ok, SanitizeStoredError(ok))
	for _, id := range []string{`a b`, `db-prod.internal`, `x\"y`, `a_b`, ``} {
		in := base + ` exchange_id="` + id + `" reason="done"`
		assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(in), id)
	}
}

// The Redshift gate texts are fixed: an appended execution id or raw text must
// not pass verbatim.
func TestSanitizeStoredError_RedshiftGateRefusalsAreClosed(t *testing.T) {
	for _, text := range []string{redshiftConflictRefusal, redshiftHorizonRefusal, redshiftNoStampRefusal, redshiftFailedConflictRefusal, redshiftNoJournalRefusal, redshiftRecentBuyRefusal, redshiftSettleRefusal, redshiftDuplicateTupleRefusal, elastiCacheDuplicatePoolRefusal, gcpDuplicateCheckFailedNote, gcpRecentCommitmentNote, elastiCacheDuplicateCheckFailedNote, elastiCacheRecentReservationNote} {
		assert.Equal(t, text, SanitizeStoredError(text))
		for _, variant := range []string{
			text + " (execution 00000000-0000-0000-0000-000000000000, running)",
			text + ": pq: connection refused",
			text + "\nraw",
		} {
			assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(variant), variant)
		}
	}
}

// Every variable slot in a closed note admits only its writer's alphabet, so
// whitespace, a line break or a control or format character never passes
// verbatim, whichever writer later fills the slot.
func TestSanitizeStoredError_SlotsRejectWhitespaceAndControls(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000000"
	resolve := `operator:completed: account="` + "1234" + "56789012" + `" region="us-east-1" checked_at=2026-01-02T03:04:05Z reason="%s"`
	slots := []string{
		"history_write_failed: commitment %s purchased but its history record failed to save",
		"execution %s: " + ErrPlanStepNoRecommendations.Error(),
		"no current recommendation matched plan %s step 2 by 2026-01-02 (3-day grace after the plan date)",
		"no current recommendation matched plan " + id + " step 2 by 2026-01-02 (3-day grace after the plan date); 1 recommendation(s) without a frozen baseline skipped (under-buy): %s",
		errAmbiguousAccountScope.Error() + ": 2 selected recommendation(s) target 2 cloud accounts (%s, " + id + "); submit one purchase per account",
		errAmbiguousAccountScope.Error() + ": 1 selected recommendation(s) carry no cloud_account_id while 2 target account %s",
		"EC2 recommendation for %s has zonal scope but no AvailabilityZone: refusing to buy a zonal RI in an arbitrary AZ; re-fetch the recommendation",
		`refusing to execute retry attempt 1: provider "%s" is not known to reject a duplicate purchase, so re-driving this could buy a second commitment`,
		`refusing to execute ladder purchase: its ladder run status "%s" is unknown`,
		resolve,
	}
	quoted := `refusing to execute ladder purchase: its ladder run status "a\"b" is unknown`
	assert.NotEqual(t, quoted, SanitizeStoredError(quoted))
	for _, bad := range []string{"a b", "a\nb", "a\rb", "a\tb", "a\vb", "a\fb", "a\u0085b", "a\u00a0b", "a\u2028b", "a\u200bb", "a\x00b"} {
		for _, slot := range slots {
			if slot == resolve && bad == "a b" {
				continue // %q keeps an ASCII space; the admin's reason may have one
			}
			in := fmt.Sprintf(slot, bad)
			assert.NotEqual(t, in, SanitizeStoredError(in), "%q", in)
		}
	}
	// A raw invalid byte or a literal U+FFFD in the reason falls back; a backslash
	// may only escape a printable ASCII character, and a commitment id stops at 256.
	for _, bad := range []string{"a\xffb", "a\ufffdb", "a\\\u202eb"} {
		in := fmt.Sprintf(resolve, bad)
		assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(in), "%q", in)
	}
	assert.Equal(t, fmt.Sprintf(resolve, `a\xffb`), SanitizeStoredError(fmt.Sprintf(resolve, `a\xffb`)), "the %q form of an invalid byte passes")
	longID := fmt.Sprintf(slots[0], strings.Repeat("a", 257))
	assert.Equal(t, ErrCategoryMessage(ErrCategoryUnknown), SanitizeStoredError(longID))
	maxID := fmt.Sprintf(slots[0], strings.Repeat("a", 256))
	assert.Equal(t, maxID, SanitizeStoredError(maxID))
	// The real alphabets still pass verbatim.
	for _, in := range []string{
		fmt.Sprintf(slots[0], "arn:aws:ec2:us-east-1:reserved/ri-0a1b.c_2"),
		fmt.Sprintf(slots[1], id),
		fmt.Sprintf(slots[2], id),
		fmt.Sprintf(slots[3], id+"/ec2/us-east-1/m5.large//1/no-upfront//Linux/UNIX/default"),
		fmt.Sprintf(slots[4], id),
		fmt.Sprintf(slots[5], id),
		fmt.Sprintf(slots[6], "m5.large"),
		fmt.Sprintf(slots[7], "oracle-cloud"),
		fmt.Sprintf(resolve, `done, see ticket \"7\"\n`),
	} {
		assert.Equal(t, in, SanitizeStoredError(in), in)
	}
}

// The direct-execute source guard's refusal (config.refuseRIExchangeSourceInFlightDirect)
// is stored whole in ri_exchange_history.error and must read back verbatim; any
// extra suffix falls back to the generic message.
func TestSanitizeStoredError_DirectExecuteGuardRefusal(t *testing.T) {
	note := "RI exchange source RI is held by an exchange in progress: source RI(s) ri-0abc, ri-0def are held by a direct exchange that may have reached AWS; an operator must verify the outcome at AWS and settle the record"
	assert.Equal(t, note, SanitizeStoredError(note))
	assert.NotEqual(t, note+": secret detail", SanitizeStoredError(note+": secret detail"))
	assert.NotEqual(t, note+"\nx", SanitizeStoredError(note+"\nx"))
}
