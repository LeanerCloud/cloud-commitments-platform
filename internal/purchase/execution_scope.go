package purchase

import (
	"context"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// recordHistoryAuditGap stamps exec.Error with a note that a successful
// purchase's history record could not be saved (issue #621). The execution
// keeps a successful status; the marker is what makes the row visible in the
// History view (which synthesizes completed executions that carry an Error).
// Appends rather than overwrites so multiple failed history writes within one
// execution are all recorded.
// historyAuditGapPrefix is the structured prefix stamped on exec.Error by
// recordHistoryAuditGap. Using a fixed prefix makes these entries queryable
// in CloudWatch Insights / log aggregators without grepping free-form text
// (05-L1). The prefix is intentionally stable; changing it invalidates any
// existing dashboards or alerts that pattern-match on it.
const historyAuditGapPrefix = "history_write_failed"

func recordHistoryAuditGap(exec *config.PurchaseExecution, commitmentID string, histErr error) {
	// histErr is a raw persistence error: log it server-side for diagnosis but
	// keep it out of exec.Error, which is surfaced to clients via the history
	// status_description. Only a generic, user-safe note reaches the UI.
	logging.Errorf("history audit gap for commitment %s: %v", commitmentID, histErr)
	// Structured marker: historyAuditGapPrefix + ":" + commitmentID so log
	// queries can filter on "history_write_failed" and operators can correlate
	// the commitment ID to the cloud provider's record without parsing prose.
	note := fmt.Sprintf("%s: commitment %s purchased but its history record failed to save",
		historyAuditGapPrefix, commitmentID)
	exec.Error = appendErrNote(exec.Error, note)
}

// idempotencyLineageKey returns the stable anchor the per-rec provider
// idempotency token is derived from (issue #1012). It prefers the durable
// IdempotencyKey column, which is generated once at first creation and copied
// verbatim onto every Retry successor and combined with the account ID for each
// multi-account fan-out row — so a strand-and-re-drive reproduces the same
// token and the provider dedupes the purchase. It falls back to ExecutionID
// only for legacy rows persisted before migration 000066 (IdempotencyKey == "");
// for a single un-retried execution that fallback is identical to the pre-fix
// behavior, and such legacy rows never gain a retry successor that could
// diverge (the retry handler seeds the successor's key from the predecessor's
// ExecutionID in that case, preserving the match).
func idempotencyLineageKey(exec *config.PurchaseExecution) string {
	if exec.IdempotencyKey != "" {
		return exec.IdempotencyKey
	}
	return exec.ExecutionID
}

// reattachAccountScope is the second #1537 gate: it decides whether a plan
// execution that carries no cloud_account_id is genuinely a root row (safe to
// fan out) or a per-account row that lost its scope in transit, and repairs the
// latter in place.
//
// How per-account-ness is detected. executeForAccount is the ONLY writer that
// puts a colon in a lineage key ("<root-key>:<accountID>", execution.go). Every
// other producer yields a colon-free value: SavePurchaseExecution mints a bare
// uuid.New().String() at first insert, persistRetryExecution copies the
// predecessor's key verbatim or falls back to its ExecutionID (also a UUID),
// and the read path just round-trips the column. (The unrelated
// api.purchaseIdempotencyKey submit-fingerprint is sha256 hex and is never
// written to this field.) So on a scopeless row a colon is by itself proof the
// row is per-account -- which is why the colon, not a match against the plan's
// current account set, is the detector. Matching the current account set would
// fail OPEN exactly when the scoped account has since been removed from the
// plan: no match, no refusal, and the row re-fans-out and double-buys.
//
// What happens then:
//
//   - Colon-free key: a real root row. Returns nil leaving CloudAccountID nil,
//     and the caller fans out as before.
//   - Terminal segment matches a current plan account: repair rather than
//     refuse. The lineage key is durable proof of the intended scope, so
//     stamping CloudAccountID makes this the single-account re-drive it was
//     always meant to be, reproducing the identical per-rec provider token.
//     That is strictly safer than erroring -- same purchase count, correct
//     dedupe -- and it is the only exit that exists: a refused row lands
//     "failed", and retrying it produces another scopeless successor carrying
//     the same key (persistRetryExecution copies both verbatim), so it would
//     be refused forever, while its predecessor is already 409 "was already
//     retried". The repair also persists: executeAndFinalize saves exec, and
//     cloud_account_id is in SavePurchaseExecution's ON CONFLICT SET.
//   - Colon present but no plan account matches: the scoped account has left
//     the plan (removed, or deleted and re-added under a new ID). There is
//     nothing to re-attach to and fanning out would re-suffix the key and
//     re-buy every account that already committed, so refuse. This is the
//     genuinely unrecoverable case and the one that must not fail open.
func reattachAccountScope(exec *config.PurchaseExecution, accounts []config.CloudAccount) error {
	key := idempotencyLineageKey(exec)
	if !strings.Contains(key, ":") {
		return nil // root key: a genuine multi-account fan-out.
	}
	for i := range accounts {
		// An empty ID would degrade the test to HasSuffix(key, ":"), so require
		// a real account ID to match on.
		if accounts[i].ID == "" {
			continue
		}
		if strings.HasSuffix(key, ":"+accounts[i].ID) {
			scoped := accounts[i].ID
			exec.CloudAccountID = &scoped
			logging.Warnf("purchase[%s]: re-attached account scope %s (%s) from the idempotency lineage key; "+
				"the row reached the executor with no cloud_account_id (issue #1537)",
				exec.ExecutionID, accounts[i].ID, accounts[i].Name)
			return nil
		}
	}
	// The key contains a colon (checked above), so the terminal segment is the
	// account it was scoped to. Naming it is what makes the error actionable:
	// that is the account to re-add.
	orphanedAccount := key[strings.LastIndex(key, ":")+1:]
	return fmt.Errorf("refusing to fan out execution %s across %d plan accounts: "+
		"its idempotency lineage key is scoped to account %s, which is no longer on plan %s, "+
		"and the row carries no cloud_account_id, so the scope cannot be recovered; "+
		"re-deriving the key would double-purchase every account that already committed (issue #1537). "+
		"Re-add account %s to the plan to let the retry complete, or cancel this execution",
		exec.ExecutionID, len(accounts), orphanedAccount, exec.PlanID, orphanedAccount)
}

// executeScopeAware dispatches a plan execution that reached the fan-out
// decision with no cloud_account_id. Split out of executePurchase so the
// #1537 scope repair does not push that function over the gocyclo budget.
func (m *Manager) executeScopeAware(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, accounts []config.CloudAccount) error {
	if err := reattachAccountScope(exec, accounts); err != nil {
		return err
	}
	if exec.CloudAccountID != nil {
		// Scope recovered from the lineage key: this is a single-account
		// re-drive, not a fan-out.
		return m.executeSingleAccount(ctx, exec, plan)
	}
	return m.executeMultiAccount(ctx, exec, plan, accounts)
}

// appendErrNote joins note onto an existing error string with "; ",
// returning note alone when existing is empty. Shared by the audit-gap and
// partial-failure paths so a successful-rec audit gap (#621) and a per-rec
// failure list (#642) within the same execution are both preserved on
// exec.Error rather than one clobbering the other.
func appendErrNote(existing, note string) string {
	if existing == "" {
		return note
	}
	if note == "" {
		return existing
	}
	return existing + "; " + note
}

// normalizePurchaseSource canonicalizes exec.Source for downstream tag
// stamping. Defense-in-depth: NormalizeSource rejects anything outside
// the allowed whitelist; an unexpected value (DB tampering, future
// code path) is dropped to "" rather than fed onto a cloud commitment
// where it would be expensive to retract.
//
// On invalid source the purchase PROCEEDS UNTAGGED (05-L3 decision):
// failing the rec over a tag-only field would abort a successful cloud
// purchase, which is a worse outcome than a missing tag. Input
// validation at the API write boundary (exec.Source on save) is the
// correct gate; this fallback is last-resort defense-in-depth.
func (m *Manager) normalizePurchaseSource(exec *config.PurchaseExecution) string {
	source := exec.Source
	if source == "" {
		return ""
	}
	normalized, err := common.NormalizeSource(source)
	if err != nil {
		logging.Warnf("Invalid purchase source %q on execution %s: %v, proceeding untagged", source, exec.ExecutionID, err)
		return ""
	}
	return normalized
}
