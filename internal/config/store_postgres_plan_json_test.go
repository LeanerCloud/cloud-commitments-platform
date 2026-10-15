package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergePlanServiceJSONFilterPresence(t *testing.T) {
	cases := []struct {
		name string
		set  func(*ServiceConfig, []string)
	}{
		{"include_engines", func(s *ServiceConfig, v []string) { s.IncludeEngines = v }},
		{"exclude_engines", func(s *ServiceConfig, v []string) { s.ExcludeEngines = v }},
		{"include_regions", func(s *ServiceConfig, v []string) { s.IncludeRegions = v }},
		{"exclude_regions", func(s *ServiceConfig, v []string) { s.ExcludeRegions = v }},
		{"include_types", func(s *ServiceConfig, v []string) { s.IncludeTypes = v }},
		{"exclude_types", func(s *ServiceConfig, v []string) { s.ExcludeTypes = v }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, values := range [][]string{{}, {"x"}, nil} {
				old := ServiceConfig{}
				var oldValue []string
				if values == nil {
					oldValue = []string{}
				}
				tc.set(&old, oldValue)
				next := old
				tc.set(&next, values)
				oldJSON, err := json.Marshal(oldValue)
				require.NoError(t, err)
				nextJSON, err := json.Marshal(values)
				require.NoError(t, err)
				raw := []byte(`{"future":9007199254740993,"` + tc.name + `":` + string(oldJSON) + `}`)
				updated, err := mergePlanServiceJSON(raw, old, next, true)
				require.NoError(t, err)
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(updated, &fields))
				require.JSONEq(t, string(nextJSON), string(fields[tc.name]))
				require.Equal(t, "9007199254740993", string(fields["future"]))
			}
		})
	}
}
