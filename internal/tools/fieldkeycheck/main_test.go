package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureSrc = `package a

type S struct {
	A int
	B string
}

type Other struct {
	X int
	Y int
}

type G[T any] struct {
	P T
	Q T
}

type Alias = S

var keyed = S{A: 1, B: "x"}
var unkeyed = S{1, "x"}
var elided = []S{{2, "y"}, {A: 3, B: "z"}}
var ptrs = []*S{{4, "p"}}
var other = Other{1, 2}
var gen = G[int]{1, 2}
var alias = Alias{5, "z"}
var anon = struct{ M, N int }{1, 2}

var table = []struct{ K, V int }{{1, 2}, {K: 3, V: 4}}
var table2 = map[string]struct{ K, V int }{"k": {5, 6}}
var table3 = []struct{ K, V int }{{K: 7, V: 8}}
var table4 = []*struct{ K, V int }{{9, 10}}

func f1() int {
	type L struct{ A, B int }
	l := L{1, 2}
	return l.A
}

func f2() int {
	type L struct{ A, B int }
	l := L{3, 4}
	return l.A
}
`

// at returns the "line:col" prefix of the first source line containing marker.
func at(t *testing.T, marker string) int {
	t.Helper()
	for i, l := range strings.Split(fixtureSrc, "\n") {
		if strings.Contains(l, marker) {
			return i + 1
		}
	}
	t.Fatalf("marker %q not in fixture", marker)
	return 0
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.23\n")
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o750); err != nil {
		t.Fatal(err)
	}
	write("a/a.go", fixtureSrc)
	in := func(marker string) string { return fmt.Sprintf("a/a.go:%d:8: struct x", at(t, marker)) }
	line := func(marker string) string { return fmt.Sprintf("a.go:%d:", at(t, marker)) }

	tests := []struct {
		name      string
		positions []string
		want      []string
		not       []string
		wantErr   bool
	}{
		{"unkeyed, elided, pointer and alias rows", []string{in("type S struct")},
			[]string{line("var unkeyed"), line("var elided"), line("var ptrs"), line("var alias")},
			[]string{line("var keyed"), line("var other"), line("var gen"), line("var anon")}, false},
		{"other struct only, not anonymous literals", []string{in("type Other")}, []string{line("var other")}, []string{line("var unkeyed"), line("var anon")}, false},
		{"generic instantiation", []string{in("type G[T")}, []string{line("var gen")}, []string{line("var unkeyed")}, false},
		{"local type L in f1 only", []string{in("type L struct{ A, B int }")},
			[]string{line("l := L{1, 2}")}, []string{line("l := L{3, 4}")}, false},
		{"anonymous struct by its own position", []string{in("var anon")}, []string{line("var anon")}, []string{line("var unkeyed")}, false},
		{"elided slice rows", []string{in("var table = ")}, []string{line("var table = ")}, []string{line("var table3"), line("var table2")}, false},
		{"elided map rows", []string{in("var table2 = ")}, []string{line("var table2")}, []string{line("var table = ")}, false},
		{"keyed elided rows", []string{in("var table3 = ")}, nil, nil, false},
		{"elided pointer rows", []string{in("var table4 = ")}, []string{line("var table4")}, nil, false},
		{"no input", nil, nil, nil, false},
		{"bogus position", []string{in("type S struct"), "a/a.go:1:1: struct x"}, nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := check(dir, "integration", tc.positions)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			joined := strings.Join(got, "\n")
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q in:\n%s", w, joined)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(joined, n) {
					t.Errorf("unexpected %q in:\n%s", n, joined)
				}
			}
			if len(tc.want) == 0 && len(got) != 0 {
				t.Errorf("want no hits, got %v", got)
			}
		})
	}
}
