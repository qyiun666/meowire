// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_test.go — WiringDiagram / Validate / RenderDiagram white-box tests.
package meowire

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qyiun666/meowire/internal/testutil"
)

// fullOrgans returns an Organs with all six required ports wired and the
// common hook pairs present, plus a working ContextBudget trimmer.
// fullHooks returns a Hooks with all eight required callbacks set (no-ops).
func fullHooks() *Hooks {
	return &Hooks{
		BeforeStimulate: func(ctx context.Context, p *Prompt) error { return nil },
		AfterStimulate:  func(ctx context.Context, output string) {},
		BeforeThink:     func(ctx context.Context, p *Prompt) error { return nil },
		AfterThink:      func(ctx context.Context, d *Decision) error { return nil },
		BeforeAct:       func(ctx context.Context, a *Action) error { return nil },
		AfterAct:        func(ctx context.Context, a *Action, e *Effect, err error) {},
		OnError:         func(ctx context.Context, err error) {},
		OnCycleEnd:      func(ctx context.Context, output string) {},
	}
}

func fullOrgans() Organs {
	return Organs{
		Think:   testutil.Thinker{Fn: func(ctx context.Context, p *Prompt) (*Decision, error) { return &Decision{Text: "ok"}, nil }},
		Act:     testutil.Effector{Fn: func(ctx context.Context, a Action) (*Effect, error) { return &Effect{Result: "ok"}, nil }},
		Closer:  &testutil.Closer{},
		Hooks:   fullHooks(),
		Sandbox: testutil.Sandbox{},
		Budget:  &ContextBudget{MaxTokens: 100, Trimmer: func(c []string, _ int) []string { return c }},
	}
}

// TestWiringDiagramFull: a complete assembly fills every slot — the six
// ports, the eight required hooks and the framework built-ins.
func TestWiringDiagramFull(t *testing.T) {
	slots := WiringDiagram(fullOrgans())
	byID := make(map[string]Slot, len(slots))
	for _, s := range slots {
		byID[s.Wire.ID] = s
	}
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6", "H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8", "F1", "G1"} {
		if !byID[id].Filled {
			t.Errorf("slot %s should be filled", id)
		}
	}
}

// TestWiringDiagramHookLevel: hook slots report filled only when the
// specific callback is present; a missing callback leaves its slot unwired
// (which Validate reports as an error — no optional hooks).
func TestWiringDiagramHookLevel(t *testing.T) {
	o := fullOrgans()
	o.Hooks = fullHooks()
	o.Hooks.BeforeThink = nil
	byID := map[string]Slot{}
	for _, s := range WiringDiagram(o) {
		byID[s.Wire.ID] = s
	}
	if byID["H3"].Filled {
		t.Error("H3 should be unwired when BeforeThink is nil")
	}
	for _, id := range []string{"H1", "H2", "H4", "H5", "H6", "H7", "H8"} {
		if !byID[id].Filled {
			t.Errorf("slot %s should stay filled when its own callback is present", id)
		}
	}
}

// TestValidateFullAssemblyClean: a complete assembly yields no error
// findings (info findings for empty Identity/Tools are expected and allowed).
func TestValidateFullAssemblyClean(t *testing.T) {
	for _, is := range Validate(fullOrgans(), Config{}) {
		if is.Level == LevelError {
			t.Errorf("unexpected error finding: %s", is.Msg)
		}
	}
}

// TestFullHooks: the helper fills every nil callback with an explicit no-op
// while keeping the declared ones; the result passes assembly validation.
func TestFullHooks(t *testing.T) {
	declared := func(ctx context.Context, p *Prompt) error { return nil }
	h := FullHooks(Hooks{BeforeThink: declared})
	if h.BeforeThink == nil {
		t.Fatal("declared BeforeThink lost")
	}
	if h.AfterThink == nil || h.OnCycleEnd == nil || h.BeforeStimulate == nil {
		t.Fatal("missing callbacks should be filled with no-ops")
	}
	o := fullOrgans()
	o.Hooks = h
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelError {
			t.Errorf("FullHooks assembly should be clean, got: %s", is.Msg)
		}
	}
}

// TestValidateRequiredMissing: every missing required slot surfaces an
// error-level finding naming the port — including every hook callback.
func TestValidateRequiredMissing(t *testing.T) {
	cases := []struct {
		name string
		miss func(o *Organs)
	}{
		{"Think", func(o *Organs) { o.Think = nil }},
		{"Act", func(o *Organs) { o.Act = nil }},
		{"Closer", func(o *Organs) { o.Closer = nil }},
		{"Hooks", func(o *Organs) { o.Hooks = nil }},
		{"Sandbox", func(o *Organs) { o.Sandbox = nil }},
		{"Budget", func(o *Organs) { o.Budget = nil }},
		{"BeforeThink", func(o *Organs) { o.Hooks.BeforeThink = nil }},
		{"OnCycleEnd", func(o *Organs) { o.Hooks.OnCycleEnd = nil }},
	}
	for _, c := range cases {
		o := fullOrgans()
		c.miss(&o)
		found := false
		for _, is := range Validate(o, Config{}) {
			if is.Level == LevelError && strings.Contains(is.Msg, c.name) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %s: no error finding mentions the port", c.name)
		}
	}
}

// TestValidateIncompleteHook: any missing hook callback is an error — there
// are no optional wiring points (explicit no-op, not absence).
func TestValidateIncompleteHook(t *testing.T) {
	o := fullOrgans()
	o.Hooks.AfterThink = nil
	found := false
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelError && strings.Contains(is.Msg, "AfterThink") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing AfterThink should surface an error finding")
	}
}

// TestValidateIncompleteBudget: a Budget without a Trimmer or MaxTokens is
// an error — a budget that does not trim is not a budget.
func TestValidateIncompleteBudget(t *testing.T) {
	o := fullOrgans()
	o.Budget = &ContextBudget{} // no trimmer, no MaxTokens
	found := false
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelError && strings.Contains(is.Msg, "Trimmer") {
			found = true
		}
	}
	if !found {
		t.Fatal("incomplete ContextBudget should surface an error finding")
	}
}

// TestValidateInfoDefaults: empty Identity/Tools/Context and default rounds
// surface info-level findings only (a clean full assembly has no error/warn).
func TestValidateInfoDefaults(t *testing.T) {
	issues := Validate(fullOrgans(), Config{})
	infos := countLevel(issues, LevelInfo)
	if infos == 0 {
		t.Fatal("default assembly should surface info findings")
	}
	for _, is := range issues {
		if is.Level != LevelInfo {
			t.Errorf("unexpected %s finding in default assembly: %s", is.Level, is.Msg)
		}
	}
}

// TestValidateParallelActsInfo: ParallelActs surfaces an info finding that
// reminds the host of the concurrency-safety prerequisite — never an error
// (the switch is opt-in behavior, not a wiring defect).
func TestValidateParallelActsInfo(t *testing.T) {
	found := false
	for _, is := range Validate(fullOrgans(), Config{ParallelActs: true}) {
		if is.Level == LevelError {
			t.Errorf("ParallelActs must never produce an error finding: %s", is.Msg)
		}
		if is.Level == LevelInfo && strings.Contains(is.Msg, "ParallelActs") {
			found = true
		}
	}
	if !found {
		t.Fatal("ParallelActs should surface an info finding")
	}
}

// TestRenderDiagram: the ASCII graph lists nodes first, then every slot
// (edge) with a fill mark and its target node.
func TestRenderDiagram(t *testing.T) {
	out := RenderDiagram(fullOrgans())
	if !strings.Contains(out, "meowire wiring graph") {
		t.Fatal("graph header missing")
	}
	if !strings.Contains(out, "nodes:\n") || !strings.Contains(out, "edges:\n") {
		t.Fatal("graph should have nodes: and edges: sections")
	}
	for _, want := range []string{"prompt", "context", "P1", "H3", "F1", "G1"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagram missing %q", want)
		}
	}
	if !strings.Contains(out, "[x]") {
		t.Error("diagram should mark filled slots with [x]")
	}
	if !strings.Contains(out, "-> context") {
		t.Error("edges should render their target node")
	}
}

// TestRenderJSON: the machine-readable graph is valid JSON carrying the
// canonical nodes and every slot edge with its filled state.
func TestRenderJSON(t *testing.T) {
	doc, err := RenderJSON(fullOrgans())
	if err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !json.Valid(doc) {
		t.Fatal("RenderJSON output is not valid JSON")
	}
	var g WiringGraph
	if err := json.Unmarshal(doc, &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(g.Nodes) != len(ConnectomeNodes()) {
		t.Errorf("json nodes = %d, want %d", len(g.Nodes), len(ConnectomeNodes()))
	}
	if len(g.Slots) != len(Connectome()) {
		t.Errorf("json slots = %d, want %d", len(g.Slots), len(Connectome()))
	}
	filled := 0
	for _, s := range g.Slots {
		if s.Filled {
			filled++
		}
	}
	if filled == 0 {
		t.Error("json graph should mark filled slots")
	}
}

// TestBuildGraph: the assembled graph carries the canonical nodes and one
// slot edge per blueprint entry.
func TestBuildGraph(t *testing.T) {
	g := BuildGraph(fullOrgans())
	if len(g.Nodes) == 0 {
		t.Fatal("graph has no nodes")
	}
	if len(g.Slots) != len(Connectome()) {
		t.Fatalf("graph slots = %d, want %d", len(g.Slots), len(Connectome()))
	}
	ids := map[string]bool{}
	for _, n := range g.Nodes {
		ids[n.ID] = true
	}
	for _, s := range g.Slots {
		if !ids[s.Wire.TargetID] {
			t.Errorf("slot %s targets unknown node %q", s.Wire.ID, s.Wire.TargetID)
		}
	}
}

// TestSlotsByTarget: the find-by-function query returns every slot touching
// a data object, and only those.
func TestSlotsByTarget(t *testing.T) {
	// Context: P6 (trim), H3 (replace) — nothing else (tool results moved to
	// the ToolResults node since v1.3.1).
	slots := SlotsByTarget(fullOrgans(), "context")
	got := map[string]bool{}
	for _, s := range slots {
		got[s.Wire.ID] = true
	}
	for _, id := range []string{"P6", "H3"} {
		if !got[id] {
			t.Errorf("Context slots missing %s", id)
		}
	}
	if len(slots) != 2 {
		t.Errorf("Context slots = %d, want 2", len(slots))
	}
	// ToolResults: F1 (append) — the single structured feedback track.
	trSlots := SlotsByTarget(fullOrgans(), "toolresults")
	trGot := map[string]bool{}
	for _, s := range trSlots {
		trGot[s.Wire.ID] = true
	}
	if !trGot["F1"] || len(trSlots) != 1 {
		t.Errorf("ToolResults slots = %v, want exactly F1", trSlots)
	}
	if len(SlotsByTarget(fullOrgans(), "plan")) != 0 {
		t.Error("plan node has no direct slots (host-side object)")
	}
}

// countLevel returns the number of issues at the given level.
func countLevel(issues []Issue, lvl IssueLevel) int {
	n := 0
	for _, is := range issues {
		if is.Level == lvl {
			n++
		}
	}
	return n
}

// TestAgentReplace: swapping the Think port via the facade takes effect at
// the next Stimulate; the previous port is returned; unknown slots error.
func TestAgentReplace(t *testing.T) {
	bp := Blueprint{Organs: fullOrgans(), Config: Config{}}
	a, err := New(bp)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	// Baseline: stubThinker returns "ok".
	var got []string
	for ev := range a.Stimulate(context.Background(), "x") {
		if ev.Kind == EventText {
			got = append(got, ev.Text)
		}
	}
	if len(got) != 1 || got[0] != "ok" {
		t.Fatalf("baseline text = %v, want [ok]", got)
	}

	old, err := a.Replace(SlotThink, testutil.Thinker{Fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return &Decision{Text: "swapped-brain"}, nil
	}})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, ok := old.(testutil.Thinker); !ok {
		t.Fatalf("old port = %T, want testutil.Thinker", old)
	}

	got = nil
	for ev := range a.Stimulate(context.Background(), "x") {
		if ev.Kind == EventText {
			got = append(got, ev.Text)
		}
	}
	if len(got) != 1 || got[0] != "swapped-brain" {
		t.Fatalf("after replace text = %v, want [swapped-brain]", got)
	}

	if _, err := a.Replace("closer", nil); err == nil {
		t.Fatal("unknown slot should error")
	}
}
