package config

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServiceTypeCollectorForms pins every const form the label guard must
// collect (or reject loudly), so a new library form is reported, not skipped.
func TestServiceTypeCollectorForms(t *testing.T) {
	tests := []struct {
		name    string
		decls   string
		want    []string
		wantErr string
	}{
		{"typed literal", `const A ServiceType = "a"`, []string{"a"}, ""},
		{"conversion call", `const A = ServiceType("a")`, []string{"a"}, ""},
		{"typed conversion call", `const A ServiceType = ServiceType("a")`, []string{"a"}, ""},
		{"grouped typed", "const (\n A ServiceType = \"a\"\n B ServiceType = \"b\"\n)", []string{"a", "b"}, ""},
		{"grouped conversion", "const (\n A = ServiceType(\"a\")\n B = ServiceType(\"b\")\n)", []string{"a", "b"}, ""},
		{"untyped literal in group is not a ServiceType", "const (\n A ServiceType = \"a\"\n B = \"b\"\n)", []string{"a"}, ""},
		{"other type ignored", `const A Other = "a"`, nil, ""},
		{"typed non-literal", `const A ServiceType = prefix + "a"`, nil, "A"},
		{"conversion non-literal", `const A = ServiceType(prefix)`, nil, "A"},
		{"iota-style inherited", "const (\n A ServiceType = iota\n B\n)", nil, "A"},
		{"implicit repetition of literal", "const (\n A ServiceType = \"a\"\n B\n)", []string{"a", "a"}, ""},
		{"unrelated non-literal", `const A = 1 + 2`, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x.go", "package common\n"+tc.decls, 0)
			require.NoError(t, err)
			got, err := serviceTypeConsts(file)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
