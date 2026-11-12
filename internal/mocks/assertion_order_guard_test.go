package mocks

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A mock assertion placed before the call under test passes for any
// implementation: the mock has recorded nothing yet, so AssertNotCalled
// succeeds and the check proves nothing (#176).
//
// Inferring "the call under test" is the hard part, so this check is
// deliberately narrow. Within one function body (nested function literals are
// their own bodies), an assertion on mock R for method M is reported when
//
//   - some later call in the body exercises R: either R.M(...) directly, or a
//     method call on a variable that was bound from an expression mentioning R
//     (client := New(R); client.Do()), and
//   - no such call precedes the assertion.
//
// AssertExpectations names no method, so any later exercise of R counts.
// Assertions after the call, and bodies that never exercise R again, are never
// reported.

var orderAssertFns = map[string]bool{
	"AssertCalled": true, "AssertNotCalled": true,
	"AssertNumberOfCalls": true, "AssertExpectations": true,
}

// mockSetupMethods are testify.Mock methods that configure a mock rather than
// exercise it.
var mockSetupMethods = map[string]bool{
	"On": true, "Test": true, "TestData": true, "Called": true, "MethodCalled": true,
}

func TestNoAssertionBeforeCallUnderTest(t *testing.T) {
	root := repoRoot(t)
	var findings []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".terraform":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			findings = append(findings, path+": parse error, guard cannot check this file: "+perr.Error())
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for _, msg := range findOrderedAssertions(fset, f) {
			findings = append(findings, rel+": "+msg)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Errorf("%d mock assertion(s) placed before the call they check:\n\n%s", len(findings), strings.Join(findings, "\n\n"))
	}
}

// findOrderedAssertions reports every assertion in f that precedes the call it
// is meant to check.
func findOrderedAssertions(fset *token.FileSet, f *ast.File) []string {
	var out []string
	var visit func(name string, body *ast.BlockStmt)
	visit = func(name string, body *ast.BlockStmt) {
		out = append(out, checkBody(fset, name, body)...)
		ast.Inspect(body, func(n ast.Node) bool {
			if fl, ok := n.(*ast.FuncLit); ok && fl.Body != body {
				visit(name, fl.Body)
				return false
			}
			return true
		})
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
			visit(fd.Name.Name, fd.Body)
		}
	}
	return out
}

type orderEvent struct {
	pos  token.Pos
	call *ast.CallExpr
}

func checkBody(fset *token.FileSet, fn string, body *ast.BlockStmt) []string {
	var calls []orderEvent
	var binds []*ast.AssignStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncLit:
			return v.Body == body
		case *ast.CallExpr:
			calls = append(calls, orderEvent{v.Pos(), v})
		case *ast.AssignStmt:
			binds = append(binds, v)
		}
		return true
	})

	var out []string
	for _, ev := range calls {
		sel, ok := ev.call.Fun.(*ast.SelectorExpr)
		if !ok || !orderAssertFns[sel.Sel.Name] {
			continue
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok {
			continue
		}
		method := ""
		if sel.Sel.Name != "AssertExpectations" {
			if len(ev.call.Args) < 2 {
				continue
			}
			lit, ok := ev.call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			method, _ = strconv.Unquote(lit.Value)
		}
		aliases := aliasesOf(recv.Name, binds, ev.pos)
		before, after := false, false
		for _, c := range calls {
			if !exercises(c.call, recv.Name, method, aliases) {
				continue
			}
			if c.pos < ev.pos {
				before = true
			} else if c.pos > ev.pos {
				after = true
			}
		}
		if after && !before {
			out = append(out, fmt.Sprintf(
				"%s: %s.%s(t, %q) in %s: the call it checks happens later in the function, "+
					"so the mock has recorded nothing yet and the assertion passes for any "+
					"implementation. Move it after the code under test.",
				fset.Position(ev.pos), recv.Name, sel.Sel.Name, method, fn))
		}
	}
	return out
}

// aliasesOf returns variables bound, before pos, from an expression that
// mentions recv: the object the mock was handed to.
func aliasesOf(recv string, binds []*ast.AssignStmt, pos token.Pos) map[string]bool {
	aliases := map[string]bool{}
	sort.Slice(binds, func(i, j int) bool { return binds[i].Pos() < binds[j].Pos() })
	for _, b := range binds {
		if b.Pos() >= pos || len(b.Lhs) == 0 {
			continue
		}
		mentions := false
		for _, r := range b.Rhs {
			ast.Inspect(r, func(n ast.Node) bool {
				if se, ok := n.(*ast.SelectorExpr); ok {
					// Only the operand counts: in f.store the field name is not a use of store.
					ast.Inspect(se.X, func(m ast.Node) bool {
						if id, ok := m.(*ast.Ident); ok && (id.Name == recv || aliases[id.Name]) {
							mentions = true
						}
						return true
					})
					return false
				}
				if id, ok := n.(*ast.Ident); ok && (id.Name == recv || aliases[id.Name]) {
					mentions = true
				}
				return true
			})
		}
		if !mentions {
			continue
		}
		for _, l := range b.Lhs {
			if id, ok := l.(*ast.Ident); ok && id.Name != "_" && id.Name != "err" && id.Name != recv {
				aliases[id.Name] = true
			}
		}
	}
	return aliases
}

// exercises reports whether call is the call under test for recv/method: a
// direct recv.method(...) (any non-setup method when method is empty), or a
// method call on an alias of recv.
func exercises(call *ast.CallExpr, recv, method string, aliases map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	root := rootIdent(sel.X)
	if root == nil {
		return false
	}
	if root.Name == recv {
		if _, direct := sel.X.(*ast.Ident); !direct {
			return false
		}
		name := sel.Sel.Name
		if strings.HasPrefix(name, "Assert") || mockSetupMethods[name] {
			return false
		}
		return method == "" || name == method
	}
	return aliases[root.Name] && !strings.HasPrefix(sel.Sel.Name, "Assert")
}

// rootIdent returns the leftmost identifier of a selector/call chain.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch v := e.(type) {
		case *ast.Ident:
			return v
		case *ast.SelectorExpr:
			e = v.X
		case *ast.CallExpr:
			e = v.Fun
		default:
			return nil
		}
	}
}

func TestAssertionOrderGuardDetects(t *testing.T) {
	const src = `package p

func TestVacuous(t *testing.T) {
	mockSP := &MockSP{}
	client := NewClient(mockSP)
	mockSP.AssertNotCalled(t, "Describe", mock.Anything)
	_, err := client.find()
	_ = err
}

func TestVacuousDirect(t *testing.T) {
	m := &MockSP{}
	m.AssertNotCalled(t, "Describe", mock.Anything)
	m.Describe(1)
}

func TestVacuousExpectations(t *testing.T) {
	m := &MockSP{}
	c := NewClient(m)
	m.AssertExpectations(t)
	c.find()
}

func TestCorrect(t *testing.T) {
	mockSP := &MockSP{}
	client := NewClient(mockSP)
	_, err := client.find()
	_ = err
	mockSP.AssertNotCalled(t, "Describe", mock.Anything)
}

func TestNoCallAtAll(t *testing.T) {
	mockSP := &MockSP{}
	mockSP.AssertNotCalled(t, "Describe", mock.Anything)
}

func TestFieldNameIsNotAMockUse(t *testing.T) {
	f := newFixture(t)
	store := f.store
	f.run()
	store.AssertNotCalled(t, "Describe", mock.Anything)
}

func TestSubtestsIndependent(t *testing.T) {
	m := &MockSP{}
	c := NewClient(m)
	t.Run("a", func(t *testing.T) {
		c.find()
		m.AssertNotCalled(t, "Describe", mock.Anything)
	})
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture_test.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := findOrderedAssertions(fset, f)
	for _, want := range []string{"in TestVacuous:", "in TestVacuousDirect:", "in TestVacuousExpectations:"} {
		hit := false
		for _, g := range got {
			hit = hit || strings.Contains(g, want)
		}
		if !hit {
			t.Errorf("detector did not fire for %s; got %v", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("findings = %d, want exactly 3 (correct, no-call, field-name and subtest shapes must not fire):\n%s",
			len(got), strings.Join(got, "\n"))
	}
}
