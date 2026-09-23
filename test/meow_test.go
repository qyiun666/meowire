// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_test.go — integration tests: New/Stimulate/Close, event consumption.
package meowire_test

import (
	"context"
	"encoding/json"
	"iter"
	"slices"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// --- shared test helpers (used by all files in this package) ---

// fullOrgans returns an Organs with all six required host ports wired plus a
// brain answering script (an empty script answers a plain "ok"; the last
// entry repeats). The brain is the bundled one — tests script its endpoint,
// they never implement a Thinker.
func fullOrgans(t *testing.T, script ...testutil.FakeCompletion) meowire.Organs {
	return meowire.Organs{
		ID:    "test-agent",
		Brain: testutil.NewFakeBrain(t, script...).Cfg(false),
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
		Closer:  &testutil.Closer{},
		Hooks:   meowire.FullHooks(meowire.Hooks{}),
		Sandbox: testutil.Sandbox{},
		Budget:  &meowire.ContextBudget{MaxTokens: 100, Trimmer: func(ctx []string, max int) []string { return ctx }, TrimResults: func(rs []meowire.ToolResult, _ int) []meowire.ToolResult { return rs }},
		Mem:     testutil.Memory{},
	}
}

// testOrgans returns fullOrgans with non-zero overrides applied.
// Script entries drive what the brain answers; a Brain field in o replaces
// the whole script (for tests that hold the server handle).
// Hooks overrides merge into the full set (testOrgans(&Hooks{...}) keeps the
// other seven callbacks) — a partial override stays a complete assembly.
func testOrgans(t *testing.T, o meowire.Organs, script ...testutil.FakeCompletion) meowire.Organs {
	base := fullOrgans(t, script...)
	if o.Brain.Model != "" {
		base.Brain = o.Brain
	}
	if o.Act != nil {
		base.Act = o.Act
	}
	if o.Closer != nil {
		base.Closer = o.Closer
	}
	if o.Hooks != nil {
		base.Hooks = meowire.FullHooks(*o.Hooks)
	}
	if o.Sandbox != nil {
		base.Sandbox = o.Sandbox
	}
	if o.Budget != nil {
		base.Budget = o.Budget
	}
	if o.Mem != nil {
		base.Mem = o.Mem
	}
	if o.ID != "" {
		base.ID = o.ID
	}
	base.System = o.System
	base.Methods = o.Methods
	base.Tools = o.Tools
	base.Context = o.Context
	base.Identity = o.Identity
	return base
}

// brainRequest decodes the n-th request body the scripted brain received, as
// far as the assertions in this package read it.
func brainRequest(t *testing.T, bodies []string, n int) (system string, user string) {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if n >= len(bodies) {
		t.Fatalf("brain request %d: only %d arrived", n, len(bodies))
	}
	if err := json.Unmarshal([]byte(bodies[n]), &req); err != nil {
		t.Fatalf("decode brain request %d: %v", n, err)
	}
	if len(req.Messages) < 2 || req.Messages[0].Role != "system" || req.Messages[1].Role != "user" {
		t.Fatalf("brain request %d messages = %+v, want [system, user, ...]", n, req.Messages)
	}
	return req.Messages[0].Content, req.Messages[1].Content
}

// testNew is a thin wrapper around meowire.New with a non-strict blueprint —
// most integration tests exercise loop behavior, not assembly strictness.
// Assembly-strictness tests call meowire.New directly with Blueprint.
func testNew(o meowire.Organs, cfg meowire.Config) (*meowire.Agent, error) {
	return meowire.New(meowire.Blueprint{Organs: o, Config: cfg})
}

// collect drains an event stream into a slice for assertions afterwards.
func collect(seq iter.Seq[meowire.Event]) []meowire.Event {
	var out []meowire.Event
	for ev := range seq {
		out = append(out, ev)
	}
	return out
}

// hasKind reports whether a stream carried an event of the given kind.
func hasKind(events []meowire.Event, k meowire.EventKind) bool {
	return slices.ContainsFunc(events, func(ev meowire.Event) bool { return ev.Kind == k })
}

// --- tests ---

// TestNewAndStimulate verifies creating an Agent and consuming events.
func TestNewAndStimulate(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{},
		testutil.FakeCompletion{Text: "meow-answer"},
	), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	events := collect(a.Stimulate(context.Background(), "work"))

	if len(events) != 5 {
		t.Fatalf("events count = %d, want 5", len(events))
	}
	if events[1].Kind != meowire.EventSandbox || events[1].Verdict.Ruling != meowire.VerdictAllow {
		t.Fatalf("events[1] = %+v, want EventSandbox(allow) from the output membrane", events[1])
	}
	if events[2].Kind != meowire.EventText || events[2].Text != "meow-answer" {
		t.Fatalf("events[2] = %+v, want EventText(meow-answer)", events[2])
	}
	if events[4].Kind != meowire.EventDone || events[4].Output != "meow-answer" {
		t.Fatalf("events[4] = %+v, want EventDone(meow-answer)", events[4])
	}

	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestResponsesModeRoundTrip: the public assembly routes the bundled brain
// onto the responses wire — the mode is a BrainConfig field the composition
// root passes through, and a two-round Stimulate (tool call, then text) runs
// the whole loop on it.
func TestResponsesModeRoundTrip(t *testing.T) {
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "working", ToolCalls: []testutil.FakeCall{{ID: "c1", Name: "tool"}}},
		testutil.FakeCompletion{Text: "ok"},
	)
	a, err := testNew(testOrgans(t, meowire.Organs{Brain: fb.CfgResponses(false)}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var texts []string
	for ev := range a.Stimulate(context.Background(), "work") {
		if ev.Kind == meowire.EventText {
			texts = append(texts, ev.Text)
		}
	}
	if !slices.Equal(texts, []string{"working", "ok"}) {
		t.Fatalf("texts = %v, want each round's text", texts)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if paths := fb.Paths(); len(paths) != 2 || paths[0] != "/responses" || paths[1] != "/responses" {
		t.Fatalf("paths = %v, want both Thinks on the responses wire", paths)
	}
}

// TestSinkThroughStimulate: a Sink mounted on the Stimulate context rides
// the whole loop to the brain — the channel is public surface, and the event
// stream still delivers the text whole (stream-and-correct).
func TestSinkThroughStimulate(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Text: "stream me"})
	a, err := testNew(testOrgans(t, meowire.Organs{Brain: fb.Cfg(true)}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	var deltas []string
	ctx := meowire.WithSink(context.Background(), func(delta string) {
		deltas = append(deltas, delta)
	})
	var texts []string
	for ev := range a.Stimulate(ctx, "work") {
		if ev.Kind == meowire.EventText {
			texts = append(texts, ev.Text)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !slices.Equal(texts, []string{"stream me"}) {
		t.Fatalf("EventText = %v, want the whole ruled segment", texts)
	}
	if len(deltas) < 2 || strings.Join(deltas, "") != "stream me" {
		t.Fatalf("sink deltas = %q, want the text in real increments through the loop", deltas)
	}
}

// TestCloseIdempotent verifies Close can be called multiple times without error.
func TestCloseIdempotent(t *testing.T) {
	a, err := testNew(testOrgans(t, meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

// TestNewRejectsMissingParams verifies New fails when any required port or
// brain parameter is missing.
func TestNewRejectsMissingParams(t *testing.T) {
	cases := []struct {
		name string
		set  func(o *meowire.Organs)
	}{
		{"Brain.Model", func(o *meowire.Organs) { o.Brain.Model = "" }},
		{"Brain.Key", func(o *meowire.Organs) { o.Brain.Key = "" }},
		{"Act", func(o *meowire.Organs) { o.Act = nil }},
		{"Closer", func(o *meowire.Organs) { o.Closer = nil }},
		{"Hooks", func(o *meowire.Organs) { o.Hooks = nil }},
		{"Sandbox", func(o *meowire.Organs) { o.Sandbox = nil }},
		{"Budget", func(o *meowire.Organs) { o.Budget = nil }},
	}
	for _, p := range cases {
		o := fullOrgans(t)
		p.set(&o)
		if _, err := testNew(o, meowire.Config{}); err == nil {
			t.Fatalf("New with missing %s should return error", p.name)
		}
	}
}

// TestOrgansID verifies the custom ID reaches the Effector via Action.CellID.
func TestOrgansID(t *testing.T) {
	var gotCellID string
	a, err := testNew(testOrgans(t, meowire.Organs{
		ID: "wired",
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			gotCellID = a.CellID
			return &meowire.Effect{Result: "ok"}, nil
		}},
	},
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "tool"}}},
		testutil.FakeCompletion{Text: "ok"},
	), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "work") {
	}
	if gotCellID != "wired" {
		t.Fatalf("CellID = %q, want %q", gotCellID, "wired")
	}
}

// TestFullOrgansWiring verifies Sandbox/Budget/Memory/Identity/Methods/Context reach the loop,
// and every prompt field the contract promises reaches the brain's request.
func TestFullOrgansWiring(t *testing.T) {
	var (
		sandboxCalled  bool
		trimmerCalled  bool
		recallCalled   bool
		rememberCalled bool
		gotFacts       meowire.CycleFacts
	)
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "t", ToolCalls: []testutil.FakeCall{{ID: "x", Name: "tool"}}},
		testutil.FakeCompletion{Text: "ok"},
	)
	a, err := testNew(testOrgans(t, meowire.Organs{
		ID:    "wired-agent",
		Brain: fb.Cfg(false),
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
		Closer: &testutil.Closer{},
		Hooks:  meowire.FullHooks(meowire.Hooks{}),
		Sandbox: testutil.Sandbox{Fn: func(ctx context.Context, a meowire.Action) (meowire.Verdict, string, error) {
			sandboxCalled = true
			return meowire.VerdictAllow, "", nil
		}},
		Budget: &meowire.ContextBudget{
			MaxTokens: 10,
			Trimmer: func(ctx []string, max int) []string {
				trimmerCalled = true
				return ctx
			},
			TrimResults: func(rs []meowire.ToolResult, _ int) []meowire.ToolResult { return rs }},
		Mem: testutil.Memory{
			RecallFn: func(context.Context, meowire.MemoryQuery) ([]meowire.Record, error) {
				recallCalled = true
				return []meowire.Record{{Key: "k1", Content: []byte("note")}}, nil
			},
			RememberFn: func(_ context.Context, f meowire.CycleFacts) error {
				rememberCalled = true
				gotFacts = f
				return nil
			},
		},
		System:   "sys",
		Methods:  []meowire.MethodSpec{{Name: "m1", Desc: "md"}},
		Tools:    []meowire.ToolSpec{{Name: "t1", Desc: "d"}},
		Context:  []string{"ctx1"},
		Identity: "wire",
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "work") {
	}
	if !sandboxCalled {
		t.Fatal("Sandbox.Allow should have been called")
	}
	if !trimmerCalled {
		t.Fatal("Budget.Trimmer should have been called")
	}
	if !recallCalled {
		t.Fatal("Memory.Recall should have been called before the first Think")
	}
	if !rememberCalled {
		t.Fatal("Memory.Remember should have been called once at the terminal")
	}
	if gotFacts.CellID != "wired-agent" || gotFacts.Input != "work" ||
		gotFacts.Output != "tok" || gotFacts.Outcome != meowire.OutcomeDone {
		t.Fatalf("Remember facts = %+v, want {wired-agent work tok Done}", gotFacts)
	}
	system, user := brainRequest(t, fb.Requests(), 0)
	if !containsAll(system, "## Identity", "wire", "- m1:", "ctx1", "note") {
		t.Fatalf("first request's system bundle = %q, want Identity/Methods/Context/Memories present", system)
	}
	if user != "work" {
		t.Fatalf("first request's user message = %q, want the stimulus", user)
	}
}

// containsAll reports whether s contains every want.
func containsAll(s string, want ...string) bool {
	for _, w := range want {
		if !strings.Contains(s, w) {
			return false
		}
	}
	return true
}
