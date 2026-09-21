// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_test.go — WiringDiagram / Validate / RenderDiagram white-box tests.
package meowire

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/qyiun666/meowire/internal/testutil"
)

// fullOrgans returns an Organs with all six required host ports wired and the
// common hook pairs present, plus a working ContextBudget trimmer and brain
// parameters that pass validation. The brain's BaseURL stays empty here: these
// tests exercise assembly, not thinking — the one that Stimulates points the
// brain at a scripted endpoint.
func fullOrgans() Organs {
	return Organs{
		ID:      "test-agent",
		Brain:   BrainConfig{Model: "fake-model", Key: "test-value-not-a-credential"},
		Act:     testutil.Effector{Fn: func(ctx context.Context, a Action) (*Effect, error) { return &Effect{Result: "ok"}, nil }},
		Closer:  &testutil.Closer{},
		Hooks:   FullHooks(Hooks{}),
		Sandbox: testutil.Sandbox{},
		Budget:  &ContextBudget{MaxTokens: 100, Trimmer: func(c []string, _ int) []string { return c }, TrimResults: func(rs []ToolResult, _ int) []ToolResult { return rs }},
		Mem:     testutil.Memory{},
	}
}

// TestWiringDiagramFull: a complete assembly fills every slot — the seven
// ports, the eight required hooks and the framework built-ins.
func TestWiringDiagramFull(t *testing.T) {
	slots := WiringDiagram(fullOrgans())
	byID := make(map[string]Slot, len(slots))
	for _, s := range slots {
		byID[s.Wire.ID] = s
	}
	for _, id := range []string{"P2", "P3", "P4", "P5", "P6", "H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8", "F1", "F2", "G1"} {
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
	o.Hooks = FullHooks(Hooks{})
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
		{"Brain.Model", func(o *Organs) { o.Brain.Model = "" }},
		{"Brain.Key", func(o *Organs) { o.Brain.Key = "" }},
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

// TestValidateIncompleteBudget: a Budget missing either trimmer or its limit
// is an error — a budget that does not trim both growing tracks is not a budget.
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

// TestValidateBudgetMissingResultTrimmer is the second track's negative case:
// a regulator that trims the text track only must still be refused, otherwise
// the unbounded growth the two-track budget exists to prevent slips back in.
func TestValidateBudgetMissingResultTrimmer(t *testing.T) {
	o := fullOrgans()
	o.Budget.TrimResults = nil
	refused := false
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelError && is.Wire == "P6" {
			refused = true
		}
	}
	if !refused {
		t.Fatal("a Budget without TrimResults must be an error finding")
	}
	if _, err := New(Blueprint{Organs: o, Config: Config{}}); err == nil {
		t.Fatal("New must reject a one-track budget")
	}
}

// TestValidateBrainModeUnknown: a Mode outside the enum is refused at
// assembly — an unknown wire is never silently reinterpreted as chat. The
// zero value, BrainModeChat and BrainModeResponses all stay clean.
func TestValidateBrainModeUnknown(t *testing.T) {
	o := fullOrgans()
	o.Brain.Mode = BrainMode(3)
	found := false
	for _, is := range Validate(o, Config{}) {
		if is.Level == LevelError && strings.Contains(is.Msg, "Brain.Mode") {
			found = true
		}
	}
	if !found {
		t.Fatal("an unknown Brain.Mode must surface an error finding")
	}
	if _, err := New(Blueprint{Organs: o, Config: Config{}}); err == nil {
		t.Fatal("New must reject an unknown Brain.Mode")
	}
	for _, mode := range []BrainMode{0, BrainModeChat, BrainModeResponses} {
		o := fullOrgans()
		o.Brain.Mode = mode
		for _, is := range Validate(o, Config{}) {
			if is.Level == LevelError {
				t.Errorf("mode %d: unexpected error finding: %s", mode, is.Msg)
			}
		}
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

// TestValidateCeilingWithoutConcurrency: a ceiling that binds nothing is
// reported rather than assumed intentional — but once the batch really runs
// concurrently the same number is a plain setting, not a finding.
func TestValidateCeilingWithoutConcurrency(t *testing.T) {
	reported := func(cfg Config) bool {
		for _, is := range Validate(fullOrgans(), cfg) {
			if strings.Contains(is.Msg, "binds nothing") {
				return true
			}
		}
		return false
	}
	if !reported(Config{MaxRounds: 4, MaxParallelActs: 2}) {
		t.Error("a ceiling set without concurrency should be reported")
	}
	if reported(Config{MaxRounds: 4, MaxParallelActs: 2, ParallelActs: true}) {
		t.Error("a ceiling that binds a real batch is not a finding")
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
	for _, want := range []string{"prompt", "context", "P2", "H3", "F1", "F2", "G1"} {
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
	var g struct {
		Nodes []WireNode
		Slots []Slot
	}
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

// TestAgentReplace: swapping the Act port via the facade takes effect at the
// next Stimulate; the previous port is returned; unknown slots error. The
// brain answers from a scripted endpoint (tool-call round, then text), so the
// swapped effector is observable.
func TestAgentReplace(t *testing.T) {
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "working", ToolCalls: []testutil.FakeCall{{ID: "c1", Name: "tool"}}},
		testutil.FakeCompletion{Text: "ok"},
		testutil.FakeCompletion{Text: "working", ToolCalls: []testutil.FakeCall{{ID: "c2", Name: "tool"}}},
		testutil.FakeCompletion{Text: "ok"},
	)
	o := fullOrgans()
	o.Brain = fb.Cfg(false)
	var first, second []string
	o.Act = testutil.Effector{Fn: func(ctx context.Context, a Action) (*Effect, error) {
		first = append(first, a.Call.Name)
		return &Effect{Result: "ok"}, nil
	}}
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer a.Close()

	var got []string
	for ev := range a.Stimulate(context.Background(), "x") {
		if ev.Kind == EventText {
			got = append(got, ev.Text)
		}
	}
	if !slices.Equal(got, []string{"working", "ok"}) {
		t.Fatalf("baseline texts = %v, want each round's text", got)
	}

	old, err := a.Replace(SlotAct, testutil.Effector{Fn: func(ctx context.Context, a Action) (*Effect, error) {
		second = append(second, a.Call.Name)
		return &Effect{Result: "swapped"}, nil
	}})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if _, ok := old.(testutil.Effector); !ok {
		t.Fatalf("old port = %T, want testutil.Effector", old)
	}

	got = nil
	for ev := range a.Stimulate(context.Background(), "x") {
		if ev.Kind == EventText {
			got = append(got, ev.Text)
		}
	}
	if !slices.Equal(got, []string{"working", "ok"}) {
		t.Fatalf("after replace texts = %v, want each round's text", got)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("effector calls = %d/%d, want the swap to take effect at the next Stimulate", len(first), len(second))
	}

	if _, err := a.Replace("closer", nil); err == nil {
		t.Fatal("unknown slot should error")
	}
}
