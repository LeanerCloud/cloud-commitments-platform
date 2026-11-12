// Command fieldkeycheck lists composite literals of the given struct types
// that are not fully keyed, so a struct can be reordered for fieldalignment
// without silently changing positional literals.
//
// Input lines look like `path:line:col: struct ...` (analyzer output), read
// from the file argument or stdin. Exit 1 when unkeyed literals are found.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

var posRe = regexp.MustCompile(`^(.+?):(\d+):\d+`)

type lineKey struct {
	file string
	line int
}

func main() {
	os.Exit(run())
}

func run() int {
	dir := flag.String("dir", ".", "module root to load ./... from")
	tags := flag.String("tags", "integration", "build tags passed to the loader")
	flag.Parse()

	var in io.Reader = os.Stdin
	if flag.NArg() > 0 {
		f, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		defer f.Close()
		in = f
	}
	positions, err := readPositions(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	hits, err := check(*dir, *tags, positions)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	for _, h := range hits {
		fmt.Println(h)
	}
	if len(hits) > 0 {
		return 1
	}
	return 0
}

func readPositions(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if posRe.MatchString(sc.Text()) {
			out = append(out, sc.Text())
		}
	}
	return out, sc.Err()
}

// canon resolves symlinks so loader paths and input paths compare equal.
func canon(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func parseTargets(dir string, positions []string) (map[lineKey]string, error) {
	targets := map[lineKey]string{}
	for _, p := range positions {
		m := posRe.FindStringSubmatch(p)
		if m == nil {
			return nil, fmt.Errorf("unparseable position %q", p)
		}
		line, err := strconv.Atoi(m[2])
		if err != nil {
			return nil, err
		}
		file := m[1]
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		targets[lineKey{canon(abs), line}] = p
	}
	return targets, nil
}

// declKey names a type by the position of its declaration, which is the same
// in every type-checking universe (Tests: true checks a package more than
// once) and distinguishes same-named local types.
func declKey(fset *token.FileSet, pos token.Pos) string {
	ps := fset.Position(pos)
	return fmt.Sprintf("%s:%d:%d", canon(ps.Filename), ps.Line, ps.Column)
}

// litKey returns the declaration key of a literal's struct type, looking
// through pointers, aliases and generic instantiations; "" when unnamed.
func litKey(fset *token.FileSet, t types.Type) string {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	if n, ok := t.(*types.Named); ok {
		return declKey(fset, n.Origin().Obj().Pos())
	}
	return ""
}

// check returns the sorted, de-duplicated positions of unkeyed literals of
// the structs declared at the given positions.
func check(dir, tags string, positions []string) ([]string, error) {
	if len(positions) == 0 {
		return nil, nil
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	targets, err := parseTargets(absDir, positions)
	if err != nil {
		return nil, err
	}
	pkgs, err := load(absDir, tags)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	resolved := map[lineKey]bool{}
	for _, p := range pkgs {
		collectTargets(p, targets, wanted, resolved)
	}
	var missing []string
	for k, orig := range targets {
		if !resolved[k] {
			missing = append(missing, orig)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("no struct declaration at: %s", strings.Join(missing, "; "))
	}
	return findUnkeyed(pkgs, wanted), nil
}

func findUnkeyed(pkgs []*packages.Package, wanted map[string]bool) []string {
	seen := map[string]bool{}
	for _, p := range pkgs {
		for _, f := range p.Syntax {
			ast.Inspect(f, func(n ast.Node) bool {
				if lit, ok := n.(*ast.CompositeLit); ok && hasUnkeyed(lit) {
					if wanted[literalKey(p, lit)] {
						seen[p.Fset.Position(lit.Pos()).String()] = true
					}
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func load(dir, tags string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:        dir,
		Tests:      true,
		BuildFlags: []string{"-tags=" + tags},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		if len(p.Errors) > 0 {
			return nil, fmt.Errorf("load %s: %w", p.PkgPath, p.Errors[0])
		}
	}
	return pkgs, nil
}

func literalKey(p *packages.Package, lit *ast.CompositeLit) string {
	if st, ok := lit.Type.(*ast.StructType); ok {
		return "anon:" + declKey(p.Fset, st.Struct)
	}
	if tv, ok := p.TypesInfo.Types[lit]; ok && tv.Type != nil {
		return litKey(p.Fset, tv.Type)
	}
	return ""
}

// collectTargets records the declared struct types (TypeSpec) at the input
// lines, plus anonymous struct types whose own position is an input.
func collectTargets(p *packages.Package, targets map[lineKey]string, wanted map[string]bool, resolved map[lineKey]bool) {
	at := func(pos token.Pos) (lineKey, bool) {
		ps := p.Fset.Position(pos)
		k := lineKey{canon(ps.Filename), ps.Line}
		_, ok := targets[k]
		return k, ok
	}
	declared := map[*ast.StructType]bool{}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.TypeSpec:
				st, ok := n.Type.(*ast.StructType)
				if !ok {
					return true
				}
				declared[st] = true
				k, hit := at(st.Struct)
				if !hit {
					k, hit = at(n.Pos())
				}
				if obj := p.TypesInfo.Defs[n.Name]; hit && obj != nil {
					wanted[declKey(p.Fset, obj.Pos())] = true
					resolved[k] = true
				}
			case *ast.StructType:
				if k, hit := at(n.Struct); hit && !declared[n] {
					wanted["anon:"+declKey(p.Fset, n.Struct)] = true
					resolved[k] = true
				}
			}
			return true
		})
	}
}

func hasUnkeyed(lit *ast.CompositeLit) bool {
	for _, e := range lit.Elts {
		if _, ok := e.(*ast.KeyValueExpr); !ok {
			return true
		}
	}
	return false
}
