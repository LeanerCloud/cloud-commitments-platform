package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every api.HandlerConfig literal in app.go (initial construction and
// reinitializeAfterConnect) must pass Insurance; dropping it from either
// silently turns the Archera routes off after the database connects.
func TestAppGoPassesInsuranceToEveryHandlerConfig(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "app.go", nil, 0)
	require.NoError(t, err)

	var found, withInsurance int
	var values []string
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandlerConfig" {
			return true
		}
		found++
		for _, el := range cl.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Insurance" {
					withInsurance++
					values = append(values, types.ExprString(kv.Value))
				}
			}
		}
		return true
	})
	assert.Equal(t, 2, found, "expected the initial and reinitializeAfterConnect HandlerConfig literals")
	assert.Equal(t, found, withInsurance, "every HandlerConfig literal must set Insurance")
	// The initial literal passes the provider it built; reinitializeAfterConnect
	// must pass that same instance (app.archera), never nil or a fresh one.
	assert.ElementsMatch(t, []string{"archeraProvider", "app.archera"}, values)
}
