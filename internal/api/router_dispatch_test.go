package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractParams_DecodesPercentEncodedID(t *testing.T) {
	r := &Router{}
	route := Route{PathPrefix: "/api/purchases/", PathSuffix: "/cancel"}
	cases := map[string]string{
		"a%3Ab%40c%2Bd%20e": "a:b@c+d e",
		"plain-uuid":        "plain-uuid",
		"bad%zz":            "bad%zz",
		"x%2Fy":             "x%2Fy",
	}
	for raw, want := range cases {
		got := r.extractParams(route, "/api/purchases/"+raw+"/cancel")["id"]
		assert.Equal(t, want, got, raw)
	}
}
