package main

import (
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

var keyed = S{A: 1, B: "x"}
var unkeyed = S{1, "x"}
var elided = []S{{2, "y"}, {A: 3, B: "z"}}
var ptrs = []*S{{4, "p"}}
var other = Other{1, 2}
`

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module fixture\n\ngo 1.21\n")
	if err := os.Mkdir(filepath.Join(dir, "a"), 0o750); err != nil {
		t.Fatal(err)
	}
	write("a/a.go", fixtureSrc)

	tests := []struct {
		name      string
		positions []string
		wantLines []string
		notLines  []string
		wantErr   bool
	}{
		{"unkeyed, elided and pointer rows reported", []string{"a/a.go:3:8: struct with 24 pointer bytes could be 8"},
			[]string{"a.go:14:", "a.go:15:", "a.go:16:"}, []string{"a.go:13:", "a.go:17:"}, false},
		{"other struct only", []string{"a/a.go:8:14: struct x"}, []string{"a.go:17:"}, []string{"a.go:14:", "a.go:15:", "a.go:16:"}, false},
		{"no input", nil, nil, nil, false},
		{"position without struct", []string{"a/a.go:1:1: struct x"}, nil, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := check(dir, "integration", tc.positions)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			joined := strings.Join(got, "\n")
			for _, w := range tc.wantLines {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q in:\n%s", w, joined)
				}
			}
			for _, n := range tc.notLines {
				if strings.Contains(joined, n) {
					t.Errorf("unexpected %q in:\n%s", n, joined)
				}
			}
			if len(tc.wantLines) == 0 && len(got) != 0 {
				t.Errorf("want no hits, got %v", got)
			}
		})
	}
}
