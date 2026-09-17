// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// size_test.go — mechanical guards for the repository's complexity budget
// (file length, function length, parameter count). loop.go grew past the file
// budget once the loop accumulated gate, pause, retry and feedback concerns;
// the split that fixed it only holds while something enforces it.
package nerve

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	maxFileLines  = 400
	maxFuncLines  = 50
	maxFuncParams = 4
)

// repoDirs are the production packages the budget covers; the working
// directory of a package test is that package, so the api facade sits two
// levels up and the sibling internal packages one level up. Test files are
// excluded by name in goFiles.
var repoDirs = []string{
	".",
	"../cell",
	"../synapse",
	"../testutil",
	"../../api",
}

// complexityExempt holds the only declarations allowed past the budget, each
// with the reason it earns the pass. Anything else must be split, not exempted.
//
//	Connectome  static data table — its length is the blueprint, not logic
//	Hebbian     host-visible learning-rule signature: (s, from, to, rate)
//	STDP        host-visible learning-rule signature: (s, from, to, dt, params)
//	Resume      host-visible resume entry point: ctx, loop context, session,
//	            response and the consumer's yield
var complexityExempt = map[string]bool{
	"Connectome": true,
	"Hebbian":    true,
	"STDP":       true,
	"Resume":     true,
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

func TestProductionFilesFitLineLimit(t *testing.T) {
	for _, dir := range repoDirs {
		for _, path := range goFiles(t, dir) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			lines := strings.Count(string(b), "\n") + 1
			if lines > maxFileLines {
				t.Errorf("%s: %d lines, budget %d — split the concern out instead of growing the file", path, lines, maxFileLines)
			}
		}
	}
}

func TestFuncDeclsFitComplexityBudget(t *testing.T) {
	fset := token.NewFileSet()
	for _, dir := range repoDirs {
		for _, path := range goFiles(t, dir) {
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				fd, ok := n.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					return true
				}
				if complexityExempt[fd.Name.Name] {
					return true
				}
				start := fset.Position(fd.Body.Pos()).Line
				end := fset.Position(fd.Body.End()).Line
				if body := end - start - 1; body > maxFuncLines {
					t.Errorf("%s: func %s body is %d lines, budget %d", path, fd.Name.Name, body, maxFuncLines)
				}
				if got := paramCount(fd); got > maxFuncParams {
					t.Errorf("%s: func %s takes %d params, budget %d — bundle them into a handle", path, fd.Name.Name, got, maxFuncParams)
				}
				return true
			})
		}
	}
}

// paramCount counts declared parameters, expanding grouped and unnamed lists.
func paramCount(fd *ast.FuncDecl) int {
	if fd.Type.Params == nil {
		return 0
	}
	n := 0
	for _, field := range fd.Type.Params.List {
		if len(field.Names) == 0 {
			n++
			continue
		}
		n += len(field.Names)
	}
	return n
}
