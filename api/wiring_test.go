// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_test.go — WiringDiagram / Validate / RenderDiagram white-box tests.
package meowire

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// --- minimal port stubs (local: api-package tests cannot reuse test/ stubs) ---

type stubThinker struct {
	fn func(ctx context.Context, p *Prompt) (*Decision, error)
}

func (s stubThinker) Think(ctx context.Context, p *Prompt) (*Decision, error) {
	if s.fn == nil {
		return &Decision{Text: "ok"}, nil
	}
	return s.fn(ctx, p)
}

type stubEffector struct {
	fn func(ctx context.Context, a Action) (*Effect, error)
}

func (s stubEffector) Act(ctx context.Context, a Action) (*Effect, error) {
	if s.fn == nil {
		return &Effect{Result: "ok"}, nil
	}
	return s.fn(ctx, a)
}

type stubCloser struct{}

func (stubCloser) Close() error { return nil }

type stubSandbox struct{}

func (stubSandbox) Allow(ctx context.Context, a Action) (bool, string, error) { return true, "", nil }
func (stubSandbox) Bounds() string                                            { return "" }

// fullOrgans returns an Organs with all six required ports wired and the
// common hook pairs present, plus a working ContextBudget trimmer.
func fullOrgans() Organs {
	return Organs{
		Think:   stubThinker{},
		Act:     stubEffector{},
		Closer:  stubCloser{},
		Hooks:   &Hooks{},
		Sandbox: stubSandbox{},
		Budget:  &ContextBudget{MaxTokens: 100, Trimmer: func(c []string, _ int) []string { return c }},
	}
}

// TestWiringDiagramFull: a complete assembly fills every required port and
// the framework built-ins; hooks remain unwired until set.
func TestWiringDiagramFull(t *testing.T) {
	slots := WiringDiagram(fullOrgans())
	byID := make(map[string]Slot, len(slots))
	for _, s := range slots {
		byID[s.Wire.ID] = s
	}
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6", "F1", "G1"} {
		if !byID[id].Filled {
			t.Errorf("slot %s should be filled", id)
		}
	}
	if byID["H3"].Filled {
		t.Error("H3 should be unwired by default")
	}
}

// TestWiringDiagramHookLevel: hook slots report filled only when the Hooks
// container and the specific callback are both present.
func TestWiringDiagramHookLevel(t *testing.T) {
	o := fullOrgans()
	o.Hooks = &Hooks{BeforeThink: func(ctx context.Context, p *Prompt) error { return nil }}
	byID := map[string]Slot{}
	for _, s := range WiringDiagram(o) {
		byID[s.Wire.ID] = s
	}
	if !byID["H3"].Filled {
		t.Error("H3 should be filled when BeforeThink is set")
	}
	if byID["H4"].Filled {
		t.Error("H4 should stay unwired when AfterThink is not set")
	}
}

// TestValidateFullAssemblyClean: a complete assembly yields no error/warn
// findings (info findings for empty Identity/Tools are expected and allowed).
func TestValidateFullAssemblyClean(t *testing.T) {
	for _, is := range Validate(fullOrgans(), Config{}) {
		if is.Level == LevelError || is.Level == LevelWarn {
			t.Errorf("unexpected %s finding: %s", is.Level, is.Msg)
		}
	}
}

// TestValidateRequiredMissing: every missing required port surfaces an
// error-level finding naming the port.
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

// TestValidateHalfWiredPairs: a Before hook without its After counterpart
// (and the reverse) surfaces a warn finding; wiring both clears it.
func TestValidateHalfWiredPairs(t *testing.T) {
	o := fullOrgans()
	o.Hooks = &Hooks{BeforeThink: func(ctx context.Context, p *Prompt) error { return nil }}
	warns := countLevel(Validate(o, Config{}), LevelWarn)
	if warns == 0 {
		t.Fatal("half-wired H3/H4 pair should warn")
	}

	o.Hooks = &Hooks{
		BeforeThink: func(ctx context.Context, p *Prompt) error { return nil },
		AfterThink:  func(ctx context.Context, d *Decision) error { return nil },
	}
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelWarn && is.Wire == "H3" {
			t.Fatalf("wired pair still warns: %s", is.Msg)
		}
	}
}

// TestValidateMemoryPath: BeforeThink without a configured ContextBudget
// trimmer warns (retrieval may overflow the context window).
func TestValidateMemoryPath(t *testing.T) {
	o := fullOrgans()
	o.Hooks = &Hooks{BeforeThink: func(ctx context.Context, p *Prompt) error { return nil }}
	o.Budget = &ContextBudget{} // no trimmer, no MaxTokens
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelWarn && strings.Contains(is.Msg, "trimmer") {
			return
		}
	}
	t.Fatal("memory path with unconfigured trimmer should warn")
}

// TestValidatePlanPath: AfterThink without BeforeThink warns because Plan
// updates cannot reach the prompt.
func TestValidatePlanPath(t *testing.T) {
	o := fullOrgans()
	o.Hooks = &Hooks{AfterThink: func(ctx context.Context, d *Decision) error { return nil }}
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelWarn && is.Wire == "H4" {
			return
		}
	}
	t.Fatal("AfterThink without BeforeThink should warn")
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
	// Context: P6 (trim), H3 (replace), F1 (append) — nothing else.
	slots := SlotsByTarget(fullOrgans(), "context")
	got := map[string]bool{}
	for _, s := range slots {
		got[s.Wire.ID] = true
	}
	for _, id := range []string{"P6", "H3", "F1"} {
		if !got[id] {
			t.Errorf("Context slots missing %s", id)
		}
	}
	if len(slots) != 3 {
		t.Errorf("Context slots = %d, want 3", len(slots))
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

	old, err := a.Replace(SlotThink, stubThinker{fn: func(ctx context.Context, p *Prompt) (*Decision, error) {
		return &Decision{Text: "swapped-brain"}, nil
	}})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, ok := old.(stubThinker); !ok {
		t.Fatalf("old port = %T, want stubThinker", old)
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
