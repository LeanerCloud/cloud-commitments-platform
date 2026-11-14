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
		wantErr string
		want    []string
	}{
		{name: "typed literal", decls: `const A ServiceType = "a"`, want: []string{"a"}, wantErr: ""},
		{name: "conversion call", decls: `const A = ServiceType("a")`, want: []string{"a"}, wantErr: ""},
		{name: "typed conversion call", decls: `const A ServiceType = ServiceType("a")`, want: []string{"a"}, wantErr: ""},
		{name: "grouped typed", decls: "const (\n A ServiceType = \"a\"\n B ServiceType = \"b\"\n)", want: []string{"a", "b"}, wantErr: ""},
		{name: "grouped conversion", decls: "const (\n A = ServiceType(\"a\")\n B = ServiceType(\"b\")\n)", want: []string{"a", "b"}, wantErr: ""},
		{name: "untyped literal in group is not a ServiceType", decls: "const (\n A ServiceType = \"a\"\n B = \"b\"\n)", want: []string{"a"}, wantErr: ""},
		{name: "other type ignored", decls: `const A Other = "a"`, want: nil, wantErr: ""},
		{name: "typed non-literal", decls: `const A ServiceType = prefix + "a"`, want: nil, wantErr: "A"},
		{name: "conversion non-literal", decls: `const A = ServiceType(prefix)`, want: nil, wantErr: "A"},
		{name: "iota-style inherited", decls: "const (\n A ServiceType = iota\n B\n)", want: nil, wantErr: "A"},
		{name: "implicit repetition of literal", decls: "const (\n A ServiceType = \"a\"\n B\n)", want: []string{"a", "a"}, wantErr: ""},
		{name: "unrelated non-literal", decls: `const A = 1 + 2`, want: nil, wantErr: ""},
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
