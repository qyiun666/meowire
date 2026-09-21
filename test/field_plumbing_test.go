// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// field_plumbing_test.go — every declared field has a path to the model.
//
// Prompt and LoopConfig are carried by hand-written copies at three places: the
// loop context that holds them, the snapshot the cell takes per invocation, and
// the request the bundled brain renders. A field declared but not copied
// compiles, vets and passes every behavioral test while never reaching the
// model or surviving an UpdateConfig — the failure is silent in exactly the
// direction a test that enumerates the production struct would also miss. So
// the expectation here is read from the struct declaration and each copy site is
// checked against it by name: adding a field is then a compile-clean event that
// this guard turns into a named omission.
package meowire_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	nervePort  = "../internal/nerve/port.go"
	nerveState = "../internal/nerve/state.go"
	nerveInvoq = "../internal/nerve/invocation.go"
	cellSrc    = "../internal/cell/cell.go"
	brainDir   = "../internal/brain"
)

// TestEveryPromptFieldReachesTheBrain: buildPrompt is the only route from the
// loop's state to the Thinker, and the brain package is the only route from a
// Prompt to a request. A field missing from either is declared, documented and
// never read.
func TestEveryPromptFieldReachesTheBrain(t *testing.T) {
	prompt := declaredFields(t, nervePort, "Prompt")

	copied := copiedKeys(t, nervePort, "buildPrompt", "Prompt")
	rendered := brainSources(t)
	for _, f := range prompt {
		if !slices.Contains(copied, f) {
			t.Errorf("%s: buildPrompt never copies Prompt.%s, so no Think sees it", nervePort, f)
		}
		if !strings.Contains(rendered, "p."+f) {
			t.Errorf("%s: no brain file reads p.%s, so the field reaches neither wire's request", brainDir, f)
		}
	}
}

// TestLoopContextHoldsEveryRoundField: the loop context is where a Prompt field
// lives between rounds and where a config field lives after UpdateConfig. A name
// missing here has no storage, and the copy sites below cannot be fixed without it.
func TestLoopContextHoldsEveryRoundField(t *testing.T) {
	lc := declaredFields(t, nerveInvoq, "LoopContext")
	sets := []struct {
		from string
		typ  string
	}{
		{nervePort, "Prompt"},
		{nerveState, "LoopConfig"},
	}
	for _, s := range sets {
		for _, f := range declaredFields(t, s.from, s.typ) {
			if !slices.Contains(lc, f) {
				t.Errorf("%s: LoopContext has no %s field, though %s.%s is copied per invocation",
					nerveInvoq, f, s.typ, f)
			}
		}
	}
}

// TestSnapshotCopiesEveryConfigField pins the one copy that UpdateConfig depends
// on: the cell snapshots the whole scalar config into each invocation, and a
// field left out of that literal is swapped at runtime and then dropped.
func TestSnapshotCopiesEveryConfigField(t *testing.T) {
	copied := copiedKeys(t, cellSrc, "snapshot", "LoopContext")
	for _, f := range declaredFields(t, nerveState, "LoopConfig") {
		if !slices.Contains(copied, f) {
			t.Errorf("%s: cell.snapshot never copies LoopConfig.%s — UpdateConfig would drop it without a word",
				cellSrc, f)
		}
	}
}

// declaredFields returns every field name of a struct type declared in a file.
func declaredFields(t *testing.T, path, typeName string) []string {
	t.Helper()
	var out []string
	walkFile(t, path, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != typeName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, fld := range st.Fields.List {
			out = append(out, identNames(fld.Names)...)
		}
		return false
	})
	if len(out) == 0 {
		t.Fatalf("%s: no fields found for struct %s — renamed or gone?", path, typeName)
	}
	return out
}

// copiedKeys returns the field names one function writes into a composite
// literal of the given type, matched by the literal's last name segment so both
// Prompt and nerve.LoopContext resolve.
func copiedKeys(t *testing.T, path, funcName, litType string) []string {
	t.Helper()
	var out []string
	found := false
	walkFile(t, path, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != funcName || fd.Body == nil {
			return true
		}
		found = true
		ast.Inspect(fd.Body, func(m ast.Node) bool {
			cl, ok := m.(*ast.CompositeLit)
			if !ok || !typeIs(cl.Type, litType) {
				return true
			}
			for _, e := range cl.Elts {
				kv, ok := e.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Key.(*ast.Ident); ok {
					out = append(out, id.Name)
				}
			}
			return false
		})
		return false
	})
	if !found {
		t.Fatalf("%s: function %s not found", path, funcName)
	}
	if len(out) == 0 {
		t.Fatalf("%s: %s writes no %s literal", path, funcName, litType)
	}
	return out
}

// brainSources is every brain file concatenated: the package renders a Prompt
// into a request between them, and which file reads which field is its own
// business.
func brainSources(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(brainDir)
	if err != nil {
		t.Fatalf("read %s: %v", brainDir, err)
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(brainDir, e.Name()))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		b.Write(src)
		n++
	}
	if n == 0 {
		t.Fatalf("%s holds no production files", brainDir)
	}
	return b.String()
}

func walkFile(t *testing.T, path string, fn func(ast.Node) bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(f, fn)
}

func typeIs(e ast.Expr, name string) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == name
	case *ast.SelectorExpr:
		return t.Sel.Name == name
	}
	return false
}

func identNames(ids []*ast.Ident) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Name)
	}
	return out
}
