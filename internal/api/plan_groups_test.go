package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func idList(n int) string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf(`"r%d"`, i)
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func TestDecodePlanRequest_Groups(t *testing.T) {
	cases := []struct {
		name, body, msg, code string
	}{
		{"flat service", `{"name":"p","service":"ec2","groups":[{"provider":"aws","service":"ec2","recommendation_ids":["a"]}]}`, "cannot be combined", planGroupsMixedWithFlatServiceCode},
		{"flat coverage", `{"name":"p","target_coverage":50,"groups":[{"recommendation_ids":["a"]}]}`, "cannot be combined", planGroupsMixedWithFlatServiceCode},
		{"empty ids", `{"name":"p","groups":[{"recommendation_ids":["a"]},{"recommendation_ids":[]}]}`, "groups[1]", ""},
		{"missing ids", `{"name":"p","groups":[{"provider":"aws"}]}`, "groups[0]", ""},
		{"1001 in group", `{"name":"p","groups":[{"recommendation_ids":` + idList(1001) + `}]}`, "groups[0]", planGroupsTooManyIDsCode},
		{"5001 total", `{"name":"p","groups":[` + strings.TrimSuffix(strings.Repeat(`{"recommendation_ids":`+idList(1000)+`},`, 5), ",") + `,{"recommendation_ids":["x"]}]}`, "in total", planGroupsTooManyTotalIDsCode},
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

func TestDecodePlanRequest_GroupsAccepted(t *testing.T) {
	body := `{"name":"p","max_step_commitment":10,"groups":[{"provider":"aws","service":"ec2","term":1,"payment":"no-upfront","coverage":80,"recommendation_ids":["a","b"]}]}`
	req, err := decodePlanRequest(body)
	require.NoError(t, err)
	require.Len(t, req.Groups, 1)
	assert.Equal(t, []string{"a", "b"}, req.Groups[0].RecommendationIDs)
}

func TestDecodePlanRequest_NoGroupsUnchanged(t *testing.T) {
	req, err := decodePlanRequest(`{"name":"p","provider":"aws","service":"ec2","term":3,"payment":"all-upfront","target_coverage":70}`)
	require.NoError(t, err)
	assert.Empty(t, req.Groups)
	assert.Equal(t, "ec2", req.Service)
}
