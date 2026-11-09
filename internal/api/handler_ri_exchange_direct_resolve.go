package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
)

// Operator list of AWS direct RI-exchange rows that still hold their source
// RIs (#1836). An accept whose outcome was lost leaves the row submitted or
// unknown, and the in-flight guard then refuses every later exchange of those
// RIs until a person who checked AWS resolves it.

const riExchangeDirectRIIDMax = 200

type riExchangeDirectListItem struct {
	ID                 string          `json:"id"`
	Status             string          `json:"status"`
	Provider           string          `json:"provider"`
	Region             string          `json:"region"`
	CloudAccountID     *string         `json:"cloud_account_id"`
	SourceIDs          []string        `json:"source_ids"`
	Targets            json.RawMessage `json:"targets"`
	MaxPaymentDue      string          `json:"max_payment_due"`
	MaxPaymentCurrency string          `json:"max_payment_currency"`
	CreatedAt          time.Time       `json:"created_at"`
	ExecutedBy         string          `json:"executed_by"`
	ErrorSummary       string          `json:"error_summary"`
	Resolvable         bool            `json:"resolvable"`
	ResolvableAt       time.Time       `json:"resolvable_at"`
}

type riExchangeDirectListResponse struct {
	Executions []riExchangeDirectListItem `json:"executions"`
	NextCursor string                     `json:"next_cursor"`
}

func parseRIExchangeDirectListQuery(q map[string]string) (config.RIExchangeDirectListFilter, error) {
	var f config.RIExchangeDirectListFilter
	if s := q["status"]; s != "" {
		if s != config.RIExchangeDirectStatusSubmitted && s != config.RIExchangeDirectStatusUnknown {
			return f, NewClientError(400, `status must be "submitted" or "unknown"`)
		}
		f.Statuses = []string{s}
	}
	if r := q["region"]; r != "" {
		if !riExchangeResolveRegionRe.MatchString(r) {
			return f, NewClientError(400, "region is not a valid AWS region")
		}
		f.Region = r
	}
	if id := q["ri_id"]; id != "" {
		if len(id) > riExchangeDirectRIIDMax || hasControlRune(id) {
			return f, NewClientError(400, "ri_id is not a valid reserved instance id")
		}
		f.RIID = id
	}
	return parseRIExchangeDirectListPaging(q, f)
}

// parseRIExchangeDirectListPaging reads the limit and cursor into f.
func parseRIExchangeDirectListPaging(q map[string]string, f config.RIExchangeDirectListFilter) (config.RIExchangeDirectListFilter, error) {
	f.Limit = config.RIExchangeDirectListDefaultLimit
	if l := q["limit"]; l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 || n > config.RIExchangeDirectListMaxLimit {
			return f, NewClientError(400, fmt.Sprintf("limit must be 1 to %d", config.RIExchangeDirectListMaxLimit))
		}
		f.Limit = n
	}
	if c := q["cursor"]; c != "" {
		cur, err := decodeRIExchangeDirectCursor(c)
		if err != nil {
			return f, err
		}
		f.After = cur
	}
	return f, nil
}

// The cursor is opaque to callers: the created_at and id of the last row of
// the previous page. It is a position, not a credential, because the store
// only ever returns rows the caller may see.
func encodeRIExchangeDirectCursor(c *config.RIExchangeDirectCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodeRIExchangeDirectCursor(s string) (*config.RIExchangeDirectCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	bad := NewClientError(400, "cursor is not valid")
	if err != nil {
		return nil, bad
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found || validateUUID(id) != nil {
		return nil, bad
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, bad
	}
	return &config.RIExchangeDirectCursor{CreatedAt: t, ID: id}, nil
}

// newRIExchangeDirectListItem is the one place a stored row becomes a response
// item, so the exposed fields are exactly these: the raw stored error, the
// executing user id and the API key id never leave the server.
func newRIExchangeDirectListItem(r *config.RIExchangeDirectRow, now time.Time) riExchangeDirectListItem {
	at := resolvableRIExchangeDirectAt(r)
	return riExchangeDirectListItem{
		ID: r.ID, Status: r.Status, Provider: string(common.ProviderAWS), Region: r.Region,
		CloudAccountID: r.CloudAccountID, SourceIDs: r.SourceIDs, Targets: r.Targets,
		MaxPaymentDue: r.MaxPaymentDue, MaxPaymentCurrency: r.MaxPaymentCurrency,
		CreatedAt: r.CreatedAt.UTC(), ExecutedBy: r.ExecutedBy,
		ErrorSummary: purchase.SanitizeStoredError(r.Error),
		Resolvable:   !now.Before(at), ResolvableAt: at,
	}
}

// resolvableRIExchangeDirectAt is when the store's rules first admit a
// resolve: a submitted row once its settle age has passed, an unknown row once
// the operator's AWS check can postdate its completion by the required margin.
func resolvableRIExchangeDirectAt(r *config.RIExchangeDirectRow) time.Time {
	if r.Status == config.RIExchangeDirectStatusSubmitted || r.CompletedAt == nil {
		return r.CreatedAt.Add(config.RIExchangeDirectResolveSubmittedAfter).UTC()
	}
	return r.CompletedAt.Add(config.RIExchangeDirectResolveUnknownCheckAfter).UTC()
}

type riExchangeDirectResolveResponse struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	ResolvedStatus string    `json:"resolved_status"`
	ResolvedAt     time.Time `json:"resolved_at"`
	Note           string    `json:"note"`
	Replayed       bool      `json:"replayed"`
}

// resolveRIExchangeDirectExecution records an operator's outcome for a stuck
// AWS direct row. Authorization runs against the ROW's account, region and
// payment cap, not the deployment's, because the row may belong to another
// account; an unknown, out-of-scope or non-AWS row is the same 404.
func (h *Handler) resolveRIExchangeDirectExecution(ctx context.Context, req *events.LambdaFunctionURLRequest, id string) (any, error) {
	session, err := h.requirePermission(ctx, req, "execute", "ri-exchange")
	if err != nil {
		return nil, err
	}
	// The resolver is stamped on the row, so it needs an identified person.
	if session.UserID == apiKeyAdminUserID {
		return nil, NewClientError(403, "resolving an RI exchange needs a signed-in user or a user API key")
	}
	if idErr := validateUUID(id); idErr != nil {
		return nil, idErr
	}
	store, ok := h.config.(config.RIExchangeDirectResolveStore)
	if !ok {
		return nil, NewClientError(503, "RI exchange resolve storage is unavailable")
	}
	row, err := store.GetRIExchangeDirectExecution(ctx, id)
	if mapped := mapRIExchangeResolveError(err); mapped != nil {
		return nil, mapped
	}
	if authErr := h.authorizeRIExchangeDirectRow(ctx, session, row); authErr != nil {
		return nil, authErr
	}
	resolve, err := buildRIExchangeDirectResolve(session, row, req.Body)
	if err != nil {
		return nil, err
	}
	res, err := store.ResolveRIExchangeDirectExecution(ctx, resolve)
	if mapped := mapRIExchangeResolveError(err); mapped != nil {
		return nil, mapped
	}
	if !res.Replayed {
		logging.Warnf("RI exchange direct row resolved by operator: id=%s actor=%q via=%s outcome=%s",
			id, session.UserID, credentialKind(ctx, session), res.ResolvedStatus)
	}
	return riExchangeDirectResolveResponse{ID: res.ID, Status: res.Status, ResolvedStatus: res.ResolvedStatus,
		ResolvedAt: res.ResolvedAt.UTC(), Note: res.Note, Replayed: res.Replayed}, nil
}

// buildRIExchangeDirectResolve validates the body against the row and builds
// the store request. The note must fit the store's byte limit, so an overlong
// reason is a clear 400 here rather than the store's.
func buildRIExchangeDirectResolve(session *Session, row *config.RIExchangeDirectRow, raw string) (config.RIExchangeDirectResolve, error) {
	body, checkedAt, err := decodeRIExchangeDirectResolve(raw, row.Region)
	if err != nil {
		return config.RIExchangeDirectResolve{}, err
	}
	note := body.note(checkedAt)
	if len(note) > config.RIExchangeDirectResolveNoteMax {
		return config.RIExchangeDirectResolve{}, NewClientError(400, "reason too long once encoded; shorten it")
	}
	return config.RIExchangeDirectResolve{
		ID: row.ID, Outcome: body.Outcome, ExpectedStatus: body.ExpectedStatus, Note: note,
		CheckedAt: checkedAt, ResolvedByUserID: resolveCreatorUserID(session), ResolvedBy: fourEyesActorIdentity(session),
	}, nil
}

// authorizeRIExchangeDirectRow applies the session's account scope and the
// grant on execute:ri-exchange to the row's own account, region and
// max_payment_due (the most the original request could have committed). A
// row outside the account scope reads as missing.
func (h *Handler) authorizeRIExchangeDirectRow(ctx context.Context, session *Session, row *config.RIExchangeDirectRow) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	account, inScope := riExchangeDirectPairAccount(scope, config.RIExchangeDirectAccountRegion{CloudAccountID: row.CloudAccountID, Region: row.Region})
	if !inScope {
		return errNotFound
	}
	return h.requireRIExchangeConstraintsFor(ctx, session, account, row.Region, func() (float64, error) {
		due, ok := parseRIExchangeAmount(row.MaxPaymentDue)
		if !ok {
			return 0, fmt.Errorf("unreadable payment cap on direct exchange %s", row.ID)
		}
		return due, nil
	})
}
