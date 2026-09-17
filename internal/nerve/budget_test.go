// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// budget_test.go — the single regulator covers both accumulating tracks.
package nerve

import (
	"context"
	"strings"
	"testing"
)

// TestBudgetTrimsBothTracks verifies one Think applies the regulator to the
// text track and the structured feedback track at the same checkpoint with the
// same limit: the declared MaxTokens has to describe everything that reaches
// the brain, not only the text Context.
func TestBudgetTrimsBothTracks(t *testing.T) {
	lc := &LoopContext{
		CellID: "c1",
		Input:  "hello",
		Think: mockThinker{fn: func(context.Context, *Prompt) (*Decision, error) {
			return &Decision{Text: "ok"}, nil
		}},
		Context:     []string{"a", "b", "c"},
		ToolResults: []ToolResult{{ID: "1", Name: "t", Result: "r"}, {ID: "2", Name: "t", Result: "r"}},
	}
	var textCalls, resultCalls int
	lc.Budget = &ContextBudget{
		MaxTokens: 7,
		Trimmer: func(c []string, max int) []string {
			textCalls++
			if max != 7 {
				t.Errorf("Trimmer got max %d, want 7", max)
			}
			return c[len(c)-1:]
		},
		TrimResults: func(r []ToolResult, max int) []ToolResult {
			resultCalls++
			if max != 7 {
				t.Errorf("TrimResults got max %d, want 7", max)
			}
			return r[len(r)-1:]
		},
	}
	fillRequired(lc)

	b := newActBatch(t.Context(), lc, &strings.Builder{}, func(Event) bool { return true })
	b.round = 1
	if _, ok := b.think(); !ok {
		t.Fatal("think should complete on a healthy assembly")
	}
	if textCalls != 1 || resultCalls != 1 {
		t.Fatalf("regulator calls = text %d, results %d; want each exactly once per Think", textCalls, resultCalls)
	}
	if len(lc.Context) != 1 || len(lc.ToolResults) != 1 {
		t.Fatalf("tracks after trim: context %d, toolResults %d; want each shrunk to 1", len(lc.Context), len(lc.ToolResults))
	}
}
