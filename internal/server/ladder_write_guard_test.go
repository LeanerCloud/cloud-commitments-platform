package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLadderWriteSideTrueHasOneCaller pins that the real write side is wired
// from exactly one place: runLadderReshape in ladder_reshape_run.go.
func TestLadderWriteSideTrueHasOneCaller(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	var trueCalls, wireCalls []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "wireLadderWriteSide":
				wireCalls = append(wireCalls, f)
			case "ladderCapability":
				if id, ok := call.Args[1].(*ast.Ident); !ok || id.Name != "false" {
					trueCalls = append(trueCalls, f)
				}
			}
			return true
		})
	}
	assert.Equal(t, []string{"ladder_write.go"}, wireCalls, "only ladderCapability may wire the write side")
	assert.Equal(t, []string{"ladder_reshape_run.go"}, trueCalls, "only runLadderReshape may pass a non-false execution flag")
}
