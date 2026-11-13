package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// idList returns a JSON array of n ids unique across calls with distinct from.
func idList(from, n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"r%d"`, from+i)
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func groupJSON(ids string) string {
	return `{"provider":"aws","service":"ec2","recommendation_ids":` + ids + `}`
}

func groupsBody(groups ...string) string {
	return `{"name":"p","groups":[` + strings.Join(groups, ",") + `]}`
}

func fiveGroupsOf1000() []string {
	gs := make([]string, 5)
	for i := range gs {
		gs[i] = groupJSON(idList(i*1000, 1000))
	}
	return gs
}

func TestDecodePlanRequest_GroupsRefused(t *testing.T) {
	six := append(fiveGroupsOf1000(), groupJSON(`["x"]`))
	cases := []struct {
		name, body, msg, code string
	}{
		{"flat service", `{"name":"p","service":"ec2","groups":[` + groupJSON(`["a"]`) + `]}`, "cannot be combined", planGroupsMixedWithFlatServiceCode},
		{"flat coverage", `{"name":"p","target_coverage":50,"groups":[` + groupJSON(`["a"]`) + `]}`, "cannot be combined", planGroupsMixedWithFlatServiceCode},
		{"empty ids", groupsBody(groupJSON(`["a"]`), groupJSON(`[]`)), "groups[1]", ""},
		{"missing ids", groupsBody(`{"provider":"aws","service":"ec2"}`), "groups[0]", ""},
		{"1001 in group", groupsBody(groupJSON(idList(0, 1001))), "groups[0]", planGroupsTooManyIDsCode},
		{"5001 total", groupsBody(six...), "in total", planGroupsTooManyTotalIDsCode},
		{"coverage 500", groupsBody(`{"provider":"aws","service":"ec2","coverage":500,"recommendation_ids":["a"]}`), "groups[0].coverage", ""},
		{"coverage -1", groupsBody(`{"provider":"aws","service":"ec2","coverage":-1,"recommendation_ids":["a"]}`), "groups[0].coverage", ""},
		{"cap 0", groupsBody(`{"provider":"aws","service":"ec2","max_step_commitment":0,"recommendation_ids":["a"]}`), "groups[0].max_step_commitment", ""},
		{"cap -1", groupsBody(`{"provider":"aws","service":"ec2","max_step_commitment":-1,"recommendation_ids":["a"]}`), "groups[0].max_step_commitment", ""},
		{"empty id", groupsBody(groupJSON(`["a",""]`)), "groups[0] has an empty", ""},
		{"null id", groupsBody(groupJSON(`["a",null]`)), "groups[0] has an empty", ""},
		{"dup within", groupsBody(groupJSON(`["a","a"]`)), `"a"`, ""},
		{"dup across", groupsBody(groupJSON(`["a"]`), groupJSON(`["b","a"]`)), `groups[1] repeats recommendation id "a"`, ""},
		{"no provider", groupsBody(`{"service":"ec2","recommendation_ids":["a"]}`), "groups[0] requires provider and service", ""},
		{"no service", groupsBody(groupJSON(`["a"]`), `{"provider":"aws","recommendation_ids":["b"]}`), "groups[1] requires provider and service", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodePlanRequest(tc.body)
			ce, ok := IsClientError(err)
			require.True(t, ok)
			assert.Equal(t, 400, ce.code)
			assert.Contains(t, ce.message, tc.msg)
			if tc.code != "" {
				assert.Equal(t, tc.code, ce.Details()["code"])
			}
		})
	}
}

func TestValidateGroups_GroupCountCap(t *testing.T) {
	groups := make([]string, maxPlanGroups+1)
	for i := range groups {
		groups[i] = `{"provider":"aws","service":"s` + fmt.Sprint(i) + `","recommendation_ids":["r` + fmt.Sprint(i) + `"]}`
	}
	_, err := decodePlanRequest(groupsBody(groups[:maxPlanGroups]...))
	assert.NoError(t, err, "exactly the cap")
	_, err = decodePlanRequest(groupsBody(groups...))
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, planGroupsTooManyCode, ce.Details()["code"])
}

func TestValidateGroups_Boundaries(t *testing.T) {
	var req PlanRequest
	require.NoError(t, json.Unmarshal([]byte(groupsBody(groupJSON(idList(0, 1000)))), &req))
	assert.NoError(t, req.validateGroups(), "exactly 1000 ids in one group")

	req = PlanRequest{}
	require.NoError(t, json.Unmarshal([]byte(groupsBody(fiveGroupsOf1000()...)), &req))
	assert.NoError(t, req.validateGroups(), "exactly 5000 ids in total")

	req = PlanRequest{}
	require.NoError(t, json.Unmarshal([]byte(groupsBody(`{"provider":"aws","service":"ec2","coverage":100,"max_step_commitment":0.01,"recommendation_ids":["a"]}`)), &req))
	assert.NoError(t, req.validateGroups(), "coverage 100 and a small positive cap")
}

func TestDecodePlanRequest_ValidGroupsAccepted(t *testing.T) {
	req, err := decodePlanRequest(groupsBody(groupJSON(`["a","b"]`)))
	require.NoError(t, err)
	require.Len(t, req.Groups, 1)
	assert.NoError(t, refuseGroupsOnUpdate(&PlanRequest{}))
	ce, ok := IsClientError(refuseGroupsOnUpdate(req))
	require.True(t, ok)
	assert.Equal(t, 400, ce.code)
	assert.Equal(t, planGroupsNotSupportedCode, ce.Details()["code"])
}

func TestDecodePlanRequest_NoGroupsUnchanged(t *testing.T) {
	req, err := decodePlanRequest(`{"name":"p","provider":"aws","service":"ec2","term":3,"payment":"all-upfront","target_coverage":70}`)
	require.NoError(t, err)
	assert.Empty(t, req.Groups)
	assert.Equal(t, "ec2", req.Service)
}
