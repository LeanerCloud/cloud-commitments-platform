package config

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEveryServiceTypeHasAFrontendLabel guards issues #1649 and #1683: every
// service slug the pinned library defines (every common.ServiceType constant)
// must have a display name in frontend/src/lib/service-labels.ts, so no page
// falls back to the raw slug. It reads the constants from the library source,
// so a constant added by a future bump fails here until it gets a label.
func TestEveryServiceTypeHasAFrontendLabel(t *testing.T) {
	slugs := libraryServiceTypes(t)
	require.Contains(t, slugs, "data-warehouse", "sanity check: the parser must find the library constants")

	src, err := os.ReadFile("../../frontend/src/lib/service-labels.ts")
	require.NoError(t, err)
	block := regexp.MustCompile(`(?s)const SERVICE_LABELS[^{]*\{(.*?)\n\};`).FindSubmatch(src)
	require.NotNil(t, block, "SERVICE_LABELS map not found in service-labels.ts")
	labeled := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s*'?([a-z0-9-]+)'?\s*:`).FindAllSubmatch(block[1], -1) {
		labeled[string(m[1])] = true
	}

	for _, slug := range slugs {
		require.Truef(t, labeled[slug], "service slug %q has no label in frontend/src/lib/service-labels.ts", slug)
	}
}

// libraryServiceTypes returns the values of every ServiceType constant in the
// pinned cloud-commitments-go common package.
func libraryServiceTypes(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/LeanerCloud/cloud-commitments-go/pkg").Output()
	require.NoError(t, err, "locate the pinned cloud-commitments-go/pkg module")
	dir := filepath.Join(strings.TrimSpace(string(out)), "common")

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)

	var slugs []string
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		found, err := serviceTypeConsts(file)
		require.NoErrorf(t, err, "parse %s", path)
		slugs = append(slugs, found...)
	}
	require.NotEmpty(t, slugs)
	return slugs
}

// serviceTypeConsts returns the string values of the ServiceType constants
// declared in file. It reads the explicit typed form (`X ServiceType = "x"`),
// the conversion form (`X = ServiceType("x")`) and specs that inherit a
// previous spec's type and values; a ServiceType constant whose value is not a
// string literal is an error, so a new declaration form fails loudly.
func serviceTypeConsts(file *ast.File) ([]string, error) {
	var slugs []string
	var errs []error
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		var typ ast.Expr
		var values []ast.Expr
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if len(vs.Values) > 0 {
				typ, values = vs.Type, vs.Values
			}
			typed := isServiceTypeIdent(typ)
			for i, v := range values {
				slug, isConversion, ok := serviceTypeLiteral(v)
				switch {
				case ok:
					if typed || isConversion {
						slugs = append(slugs, slug)
					}
				case typed || isConversion:
					name := "?"
					if i < len(vs.Names) {
						name = vs.Names[i].Name
					}
					errs = append(errs, fmt.Errorf("ServiceType constant %s has a value the label guard cannot read; teach serviceTypeConsts this form", name))
				}
			}
		}
	}
	return slugs, errors.Join(errs...)
}

func isServiceTypeIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "ServiceType"
}

// serviceTypeLiteral unquotes v when it is a string literal or a
// ServiceType("literal") conversion. isConversion reports a ServiceType(...)
// call, which is a ServiceType constant even when its argument is unreadable.
func serviceTypeLiteral(v ast.Expr) (slug string, isConversion, ok bool) {
	arg := v
	if call, isCall := v.(*ast.CallExpr); isCall && isServiceTypeIdent(call.Fun) {
		isConversion = true
		if len(call.Args) != 1 {
			return "", true, false
		}
		arg = call.Args[0]
	}
	lit, isLit := arg.(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", isConversion, false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, isConversion, err == nil
}
