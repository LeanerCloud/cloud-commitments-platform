package mocks

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// handRolledConfigStoreAllowlist lists hand-rolled config.StoreInterface copies
// that are still waiting to be folded onto MockConfigStore (issue #76). It is
// empty now that internal/server.mockConfigStoreForHealth embeds
// MockConfigStore, and it may only shrink: an entry whose type no longer
// hand-rolls the interface fails the guard until it is removed here.
var handRolledConfigStoreAllowlist = map[string]bool{}

// TestNoHandRolledConfigStoreMocks keeps test doubles of config.StoreInterface
// from being written out method by method. Every such copy needs a new stub for
// every new store method, which is the per-method tax issue #76 describes. A
// test double should embed MockConfigStore (or the interface) and override only
// the methods its test exercises.
//
// The check is syntactic: a type declared in a _test.go file that defines more
// than half of StoreInterface's methods is a hand-rolled copy. Partial
// overrides on top of an embedded store define a handful.
func TestNoHandRolledConfigStoreMocks(t *testing.T) {
	root := repoRoot(t)
	ifaceMethods := storeInterfaceMethods(t, filepath.Join(root, "internal", "config"))
	if len(ifaceMethods) < 50 {
		t.Fatalf("parsed only %d StoreInterface methods; the interface lookup has broken", len(ifaceMethods))
	}

	defined := map[string]map[string]bool{} // "<dir>.<type>" -> StoreInterface methods it defines
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", ".terraform", "frontend":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // a file that does not parse fails its own package's build
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 || !ifaceMethods[fd.Name.Name] {
				continue
			}
			typeName := recvTypeIdent(fd.Recv.List[0].Type)
			if typeName == "" {
				continue
			}
			key := filepath.ToSlash(rel) + "." + typeName
			if defined[key] == nil {
				defined[key] = map[string]bool{}
			}
			defined[key][fd.Name.Name] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	var copies []string
	for key, methods := range defined {
		handRolled := len(methods)*2 > len(ifaceMethods)
		switch {
		case handRolled && !handRolledConfigStoreAllowlist[key]:
			copies = append(copies, key)
		case !handRolled && handRolledConfigStoreAllowlist[key]:
			t.Errorf("%s no longer hand-rolls config.StoreInterface; remove it from handRolledConfigStoreAllowlist", key)
		}
	}
	for key := range handRolledConfigStoreAllowlist {
		if defined[key] == nil {
			t.Errorf("%s is allowlisted but defines no StoreInterface method; remove it from handRolledConfigStoreAllowlist", key)
		}
	}
	if len(copies) > 0 {
		sort.Strings(copies)
		t.Errorf("hand-rolled config.StoreInterface test doubles (embed mocks.MockConfigStore and override only "+
			"what the test needs instead):\n  %s", strings.Join(copies, "\n  "))
	}
}

// storeInterfaceMethods returns the method names of config.StoreInterface,
// including the methods of the named interfaces it embeds. dir is the
// directory of the config package; the interfaces are looked up in its non-test
// files.
func storeInterfaceMethods(t *testing.T, dir string) map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	ifaces := map[string]*ast.InterfaceType{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					if it, ok := ts.Type.(*ast.InterfaceType); ok {
						ifaces[ts.Name.Name] = it
					}
				}
			}
		}
	}
	methods := map[string]bool{}
	var collect func(name string)
	collect = func(name string) {
		it, ok := ifaces[name]
		if !ok {
			t.Fatalf("interface %s not found in %s", name, dir)
		}
		for _, field := range it.Methods.List {
			if len(field.Names) == 0 {
				id, ok := field.Type.(*ast.Ident)
				if !ok {
					t.Fatalf("%s embeds a non-identifier type; the interface lookup cannot follow it", name)
				}
				collect(id.Name)
				continue
			}
			for _, n := range field.Names {
				methods[n.Name] = true
			}
		}
	}
	collect("StoreInterface")
	return methods
}
