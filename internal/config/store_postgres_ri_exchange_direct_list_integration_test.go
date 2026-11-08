//go:build integration

package config

import (
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Real-PostgreSQL coverage for the #1836 list and get reads.

// directSpec describes one audit row for the list tests.
type directSpec struct {
	provider, status string
	account          *string
	region           string
	sources          []string
	createdAt        time.Time
	nullRegion       bool
	resolved         bool
	errText          string
}

// insertDirect writes an audit row exactly as described and returns its id.
func (e *riExchangeIntegrationEnv) insertDirect(t *testing.T, s directSpec) string {
	t.Helper()
	if s.provider == "" {
		s.provider = "aws"
	}
	if s.status == "" {
		s.status = "unknown"
	}
	var region *string
	if s.region != "" || (s.provider == "aws" && !s.nullRegion) {
		if s.region == "" {
			s.region = "eu-west-1"
		}
		region = &s.region
	}
	if len(s.sources) == 0 {
		s.sources = []string{"ri-" + uuid.NewString()}
	}
	if s.createdAt.IsZero() {
		s.createdAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	var errText *string
	if s.errText != "" {
		errText = &s.errText
	}
	var id string
	require.NoError(t, e.poolA.QueryRow(e.ctx, `
		INSERT INTO ri_exchange_direct_executions
			(provider, cloud_account_id, region, source_ids, targets, max_payment_due, max_payment_currency,
			 status, error, executed_by, created_at, completed_at,
			 resolved_status, resolved_by, resolved_at, resolution_note)
		VALUES ($1, $2, $3, $4, '[{"offering_id":"o","count":1}]'::jsonb, 100, 'USD',
			$5, $6, 'tester', $7, CASE WHEN $5 = 'submitted' THEN NULL ELSE $7::timestamptz END,
			CASE WHEN $8 THEN 'failed' END, CASE WHEN $8 THEN 'op' END, CASE WHEN $8 THEN now() END, CASE WHEN $8 THEN 'note' END)
		RETURNING id::text`,
		s.provider, s.account, region, s.sources, s.status, errText, s.createdAt, s.resolved).Scan(&id))
	return id
}

func rowIDs(rows []RIExchangeDirectRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

func allow(account *string, region string) []RIExchangeDirectAccountRegion {
	return []RIExchangeDirectAccountRegion{{CloudAccountID: account, Region: region}}
}

// [fail-first] Only unresolved submitted and unknown AWS rows are listed.
func TestPostgresStoreDB_RIExchangeDirectList_OnlyUnresolvedAWSInFlight(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	acc := strPtr("acc-1")
	want := []string{
		env.insertDirect(t, directSpec{status: "submitted", account: acc}),
		env.insertDirect(t, directSpec{status: "unknown", account: acc}),
	}
	env.insertDirect(t, directSpec{provider: "azure", status: "unknown", account: acc, region: "eu-west-1"}) // same account and region, so only the provider predicate excludes it
	env.insertDirect(t, directSpec{status: "completed", account: acc})
	env.insertDirect(t, directSpec{status: "failed", account: acc})
	env.insertDirect(t, directSpec{status: "unknown", account: acc, resolved: true})
	env.insertDirect(t, directSpec{status: "submitted", account: acc, resolved: true})

	page, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allow(acc, "eu-west-1")})
	require.NoError(t, err)
	assert.ElementsMatch(t, want, rowIDs(page.Rows))
	assert.Nil(t, page.Next)
}

// [fail-first] The status, region and normalised ri_id filters.
func TestPostgresStoreDB_RIExchangeDirectList_Filters(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	acc := strPtr("acc-1")
	sub := env.insertDirect(t, directSpec{status: "submitted", account: acc, sources: []string{"ri-a"}})
	unk := env.insertDirect(t, directSpec{status: "unknown", account: acc, sources: []string{" RI-PADDED ", "ri-b"}})
	other := env.insertDirect(t, directSpec{status: "unknown", account: acc, region: "us-east-1", sources: []string{"ri-c"}})
	allowed := []RIExchangeDirectAccountRegion{{acc, "eu-west-1"}, {acc, "us-east-1"}}
	list := func(f RIExchangeDirectListFilter) []string {
		f.Allowed = allowed
		page, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, f)
		require.NoError(t, err)
		ids := rowIDs(page.Rows)
		sort.Strings(ids)
		return ids
	}
	sorted := func(ids ...string) []string { sort.Strings(ids); return ids }

	assert.Equal(t, sorted(sub, unk, other), list(RIExchangeDirectListFilter{}), "default is both statuses")
	assert.Equal(t, []string{sub}, list(RIExchangeDirectListFilter{Statuses: []string{"submitted"}}))
	assert.Equal(t, sorted(unk, other), list(RIExchangeDirectListFilter{Statuses: []string{"unknown"}}))
	assert.Equal(t, []string{other}, list(RIExchangeDirectListFilter{Region: "us-east-1"}))
	assert.Equal(t, []string{unk}, list(RIExchangeDirectListFilter{RIID: "ri-padded"}), "stored padded upper case matches")
	assert.Equal(t, []string{unk}, list(RIExchangeDirectListFilter{RIID: "  Ri-B "}), "padded mixed-case request matches")
	assert.Empty(t, list(RIExchangeDirectListFilter{RIID: "ri-none"}))

	_, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allowed, Statuses: []string{"completed"}})
	require.ErrorIs(t, err, ErrRIExchangeResolveInvalid)
	_, err = env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allowed, Limit: 201})
	require.ErrorIs(t, err, ErrRIExchangeResolveInvalid)
}

// [fail-first] The allowed (account, region) set bounds the page, a NULL
// account matches only a NULL entry, and an empty set lists nothing.
func TestPostgresStoreDB_RIExchangeDirectList_AllowedAccountRegionSet(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	a1, a2 := strPtr("acc-1"), strPtr("acc-2")
	r1 := env.insertDirect(t, directSpec{account: a1})
	r2 := env.insertDirect(t, directSpec{account: a2})
	rOtherRegion := env.insertDirect(t, directSpec{account: a1, region: "us-east-1"})
	rNull := env.insertDirect(t, directSpec{account: nil})
	list := func(allowed []RIExchangeDirectAccountRegion) []string {
		page, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allowed})
		require.NoError(t, err)
		ids := rowIDs(page.Rows)
		sort.Strings(ids)
		return ids
	}
	sorted := func(ids ...string) []string { sort.Strings(ids); return ids }

	assert.Equal(t, []string{r1}, list(allow(a1, "eu-west-1")), "one account, one region")
	assert.Equal(t, sorted(r1, r2), list([]RIExchangeDirectAccountRegion{{a1, "eu-west-1"}, {a2, "eu-west-1"}}))
	assert.Equal(t, sorted(r1, rOtherRegion), list([]RIExchangeDirectAccountRegion{{a1, "eu-west-1"}, {a1, "us-east-1"}}))
	assert.Equal(t, []string{rNull}, list(allow(nil, "eu-west-1")), "a NULL account is listed only for a NULL entry")
	assert.Empty(t, list(allow(strPtr("acc-3"), "eu-west-1")))
	assert.Empty(t, list(allow(a1, "ap-south-1")), "right account, wrong region")
	assert.Empty(t, list(nil), "an empty set lists nothing")
}

// [fail-first] Keyset pages are complete and ordered with equal created_at.
func TestPostgresStoreDB_RIExchangeDirectList_KeysetPagination(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	acc := strPtr("acc-1")
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	var all []string
	for _, at := range []time.Time{base, base, base, base, base.Add(time.Second), base.Add(time.Second), base.Add(2 * time.Second)} {
		all = append(all, env.insertDirect(t, directSpec{account: acc, createdAt: at}))
	}
	type key struct {
		at time.Time
		id string
	}
	var keys []key
	full, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allow(acc, "eu-west-1")})
	require.NoError(t, err)
	for _, r := range full.Rows {
		keys = append(keys, key{r.CreatedAt, r.ID})
	}
	require.Len(t, keys, len(all))
	require.True(t, sort.SliceIsSorted(keys, func(i, j int) bool {
		if !keys[i].at.Equal(keys[j].at) {
			return keys[i].at.Before(keys[j].at)
		}
		return keys[i].id < keys[j].id
	}), "oldest first, id breaks ties")

	var got []string
	var after *RIExchangeDirectCursor
	for pages := 0; pages < 10; pages++ {
		page, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx,
			RIExchangeDirectListFilter{Allowed: allow(acc, "eu-west-1"), Limit: 2, After: after})
		require.NoError(t, err)
		got = append(got, rowIDs(page.Rows)...)
		if page.Next == nil {
			break
		}
		require.Len(t, page.Rows, 2)
		after = page.Next
	}
	assert.Equal(t, rowIDs(full.Rows), got, "pages concatenate to the full ordered list with no gap or repeat")
}

// [fail-first] An AWS row with a NULL region is listed for the empty region,
// the same pair the pair read reports for it.
func TestPostgresStoreDB_RIExchangeDirectList_NullRegionRowIsListable(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	acc := strPtr("acc-1")
	id := env.insertDirect(t, directSpec{account: acc, nullRegion: true})
	env.insertDirect(t, directSpec{account: acc})

	page, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, RIExchangeDirectListFilter{Allowed: allow(acc, "")})
	require.NoError(t, err)
	require.Equal(t, []string{id}, rowIDs(page.Rows))
	assert.Equal(t, "", page.Rows[0].Region)
}

// [fail-first] A bad filter is invalid even when nothing is allowed, never an
// empty result.
func TestPostgresStoreDB_RIExchangeDirectList_BadFilterIsInvalidWithEmptyAllowSet(t *testing.T) {
	env := newRIExchangeIntegrationEnv(t)
	for name, f := range map[string]RIExchangeDirectListFilter{
		"limit":  {Limit: 201},
		"status": {Statuses: []string{"completed"}},
	} {
		_, err := env.storeA.ListUnresolvedRIExchangeDirectExecutions(env.ctx, f)
		require.ErrorIs(t, err, ErrRIExchangeResolveInvalid, name)
	}
}
