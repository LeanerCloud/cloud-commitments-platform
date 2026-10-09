package server

import (
	"go/ast"
	"go/parser"
	"go/token"
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
				}
			}
		}
		return true
	})
	assert.Equal(t, 2, found, "expected the initial and reinitializeAfterConnect HandlerConfig literals")
	assert.Equal(t, found, withInsurance, "every HandlerConfig literal must set Insurance")
}
