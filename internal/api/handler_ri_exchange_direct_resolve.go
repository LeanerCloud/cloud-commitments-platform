package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
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
		SourceIDs: r.SourceIDs, Targets: r.Targets,
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
