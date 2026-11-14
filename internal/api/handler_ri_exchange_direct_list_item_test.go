package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
)

// Unit tests for the pieces of GET /api/ri-exchange/direct-executions that need
// no store (#1836): what a stored row exposes, the resolvable hint, query
// validation and the cursor.

const (
	dlAcctA = scopedInAccount
	dlAcctB = scopedOutAccount
	dlID1   = "00000000-0000-4000-8000-000000000001"
	dlID2   = "00000000-0000-4000-8000-000000000002"
	dlID3   = "00000000-0000-4000-8000-000000000003"
	dlID4   = "00000000-0000-4000-8000-000000000004"
)

var dlBase = time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)

func dlPtr(s string) *string { return &s }

func dlRow(id string, account *string, region, status string, minutes int) config.RIExchangeDirectRow {
	at := dlBase.Add(time.Duration(minutes) * time.Minute)
	r := config.RIExchangeDirectRow{
		ID: id, Status: status, CloudAccountID: account, Region: region,
		SourceIDs: []string{"ri-" + id}, Targets: json.RawMessage(`[{"offering_id":"o","count":1}]`),
		MaxPaymentDue: "100", MaxPaymentCurrency: "USD", ExecutedBy: "alice@example.com", CreatedAt: at,
	}
	if status != "submitted" {
		r.CompletedAt = &at
	}
	return r
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// [fail-first] The response carries exactly the allowlisted keys: never the raw
// error, the executing user id or the API key id.
func TestRIExchangeDirectListItem_KeysAreAllowlisted(t *testing.T) {
	t.Parallel()
	row := dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1)
	row.Error = "InvalidParameterValue for account 123456789012 from 10.1.2.3: raw provider text"

	raw, err := json.Marshal(riExchangeDirectListResponse{
		Executions: []riExchangeDirectListItem{newRIExchangeDirectListItem(&row, time.Now())}, NextCursor: ""})
	require.NoError(t, err)

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &top))
	assert.ElementsMatch(t, []string{"executions", "next_cursor"}, keysOf(top))
	var items []map[string]any
	require.NoError(t, json.Unmarshal(top["executions"], &items))
	require.Len(t, items, 1)
	assert.ElementsMatch(t, []string{"id", "status", "provider", "region", "cloud_account_id", "source_ids", "targets", "max_payment_due",
		"max_payment_currency", "created_at", "executed_by", "error_summary", "resolvable", "resolvable_at"}, keysOf(items[0]))

	assert.Equal(t, purchase.SanitizeStoredError(row.Error), items[0]["error_summary"])
	for _, leak := range []string{"123456789012", "10.1.2.3", "raw provider text"} {
		assert.NotContains(t, string(raw), leak)
	}
	assert.Equal(t, "aws", items[0]["provider"])
}

func TestRIExchangeDirectListItem_CloudAccountID(t *testing.T) {
	t.Parallel()
	attributed := dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1)
	unattributed := dlRow(dlID2, nil, "eu-west-1", "unknown", 1)

	raw, err := json.Marshal(newRIExchangeDirectListItem(&attributed, time.Now()))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"cloud_account_id":"`+dlAcctA+`"`)

	raw, err = json.Marshal(newRIExchangeDirectListItem(&unattributed, time.Now()))
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"cloud_account_id":null`)
}

func TestRIExchangeDirectListItem_ResolvableHint(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	at := func(r config.RIExchangeDirectRow, created time.Time) config.RIExchangeDirectRow {
		r.CreatedAt = created
		if r.CompletedAt != nil {
			r.CompletedAt = &created
		}
		return r
	}
	sub := at(dlRow(dlID1, nil, "eu-west-1", "submitted", 0), now.Add(-5*time.Minute))
	oldSub := at(dlRow(dlID2, nil, "eu-west-1", "submitted", 0), now.Add(-config.RIExchangeDirectResolveSubmittedAfter-time.Minute))
	unk := at(dlRow(dlID3, nil, "eu-west-1", "unknown", 0), now.Add(-2*time.Minute))
	freshUnk := at(dlRow(dlID4, nil, "eu-west-1", "unknown", 0), now.Add(-10*time.Second))

	got := func(r config.RIExchangeDirectRow) riExchangeDirectListItem {
		return newRIExchangeDirectListItem(&r, now)
	}
	assert.False(t, got(sub).Resolvable)
	assert.True(t, got(sub).ResolvableAt.Equal(sub.CreatedAt.Add(config.RIExchangeDirectResolveSubmittedAfter)))
	assert.True(t, got(oldSub).Resolvable)
	assert.True(t, got(unk).Resolvable)
	assert.True(t, got(unk).ResolvableAt.Equal(unk.CompletedAt.Add(config.RIExchangeDirectResolveUnknownCheckAfter)))
	assert.False(t, got(freshUnk).Resolvable)
}

func TestParseRIExchangeDirectListQuery(t *testing.T) {
	t.Parallel()
	for name, q := range map[string]map[string]string{
		"bad status":     {"status": "completed"},
		"limit zero":     {"limit": "0"},
		"limit too big":  {"limit": "201"},
		"limit not int":  {"limit": "ten"},
		"bad region":     {"region": "EU West"},
		"bad cursor":     {"cursor": "!!!"},
		"cursor no id":   {"cursor": "MjAyNi0wMS0wMVQwMDowMDowMFp8bm90LWEtdXVpZA"},
		"ri_id control":  {"ri_id": "ri\n1"},
		"ri_id too long": {"ri_id": strings.Repeat("a", riExchangeDirectRIIDMax+1)},
	} {
		_, err := parseRIExchangeDirectListQuery(q)
		assert.Equal(t, 400, clientCode(t, err), name)
	}

	f, err := parseRIExchangeDirectListQuery(map[string]string{"status": "unknown", "region": "eu-west-1", "ri_id": "RI-X", "limit": "7"})
	require.NoError(t, err)
	assert.Equal(t, []string{"unknown"}, f.Statuses)
	assert.Equal(t, "eu-west-1", f.Region)
	assert.Equal(t, "RI-X", f.RIID)
	assert.Equal(t, 7, f.Limit)

	f, err = parseRIExchangeDirectListQuery(nil)
	require.NoError(t, err)
	assert.Equal(t, config.RIExchangeDirectListDefaultLimit, f.Limit)
}

func TestRIExchangeDirectCursor_RoundTripKeepsMicroseconds(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 7, 10, 20, 30, 123456000, time.UTC)
	got, err := decodeRIExchangeDirectCursor(encodeRIExchangeDirectCursor(&config.RIExchangeDirectCursor{CreatedAt: at, ID: dlID2}))
	require.NoError(t, err)
	assert.True(t, got.CreatedAt.Equal(at))
	assert.Equal(t, dlID2, got.ID)
}
