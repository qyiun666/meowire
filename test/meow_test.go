// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// meow_test.go — integration tests: New/Stimulate/Close, event consumption.
package meowire_test

import (
	"context"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// --- shared test helpers (used by all files in this package) ---

// allowSandbox permits all tool calls.
type allowSandbox struct{}

func (allowSandbox) Allow(ctx context.Context, a meowire.Action) (bool, string, error) {
	return true, "", nil
}

func (allowSandbox) Bounds() string { return "" }

// sandboxFn is a Sandbox stub driven by a function field.
type sandboxFn struct {
	fn func(ctx context.Context, a meowire.Action) (bool, string, error)
}

func (s sandboxFn) Allow(ctx context.Context, a meowire.Action) (bool, string, error) {
	return s.fn(ctx, a)
}

func (sandboxFn) Bounds() string { return "" }

// closerStub records Close invocations.
type closerStub struct{ called bool }

func (c *closerStub) Close() error {
	c.called = true
	return nil
}

// fullOrgans returns an Organs with all six required ports wired.
func fullOrgans() meowire.Organs {
	return meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "ok"}, nil
		}},
		Act: &fnEffector{fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
		Closer:  &closerStub{},
		Hooks:   &meowire.Hooks{},
		Sandbox: allowSandbox{},
		Budget:  &meowire.ContextBudget{MaxTokens: 100, Trimmer: func(ctx []string, max int) []string { return ctx }},
	}
}

// testOrgans returns fullOrgans with non-zero overrides applied.
func testOrgans(o meowire.Organs) meowire.Organs {
	base := fullOrgans()
	if o.Think != nil {
		base.Think = o.Think
	}
	if o.Act != nil {
		base.Act = o.Act
	}
	if o.Closer != nil {
		base.Closer = o.Closer
	}
	if o.Hooks != nil {
		base.Hooks = o.Hooks
	}
	if o.Sandbox != nil {
		base.Sandbox = o.Sandbox
	}
	if o.Budget != nil {
		base.Budget = o.Budget
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

// testNew is a thin wrapper around meowire.New with a non-strict blueprint —
// most integration tests exercise loop behavior, not assembly strictness.
// Assembly-strictness tests call meowire.New directly with Blueprint.
func testNew(o meowire.Organs, cfg meowire.Config) (*meowire.Agent, error) {
	return meowire.New(meowire.Blueprint{Organs: o, Config: cfg})
}

// --- inline mock ports ---

type fnThinker struct {
	fn func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error)
}

func (t *fnThinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	return t.fn(ctx, p)
}

type fnEffector struct {
	fn func(ctx context.Context, a meowire.Action) (*meowire.Effect, error)
}

func (e *fnEffector) Act(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
	return e.fn(ctx, a)
}

// --- tests ---

// TestNewAndStimulate verifies creating an Agent and consuming events.
func TestNewAndStimulate(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "meow-answer"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []meowire.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}

	if len(events) != 4 {
		t.Fatalf("events count = %d, want 4", len(events))
	}
	if events[1].Kind != meowire.EventText || events[1].Text != "meow-answer" {
		t.Fatalf("events[1] = %+v, want EventText(meow-answer)", events[1])
	}
	if events[3].Kind != meowire.EventDone || events[3].Output != "meow-answer" {
		t.Fatalf("events[3] = %+v, want EventDone(meow-answer)", events[3])
	}

	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestCloseIdempotent verifies Close can be called multiple times without error.
func TestCloseIdempotent(t *testing.T) {
	a, err := testNew(testOrgans(meowire.Organs{}), meowire.Config{})
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

// TestNewRejectsMissingPorts verifies New fails when any required port is missing.
func TestNewRejectsMissingPorts(t *testing.T) {
	ports := []struct {
		name string
		set  func(o *meowire.Organs)
	}{
		{"Think", func(o *meowire.Organs) { o.Think = nil }},
		{"Act", func(o *meowire.Organs) { o.Act = nil }},
		{"Closer", func(o *meowire.Organs) { o.Closer = nil }},
		{"Hooks", func(o *meowire.Organs) { o.Hooks = nil }},
		{"Sandbox", func(o *meowire.Organs) { o.Sandbox = nil }},
		{"Budget", func(o *meowire.Organs) { o.Budget = nil }},
	}
	for _, p := range ports {
		o := fullOrgans()
		p.set(&o)
		if _, err := testNew(o, meowire.Config{}); err == nil {
			t.Fatalf("New with missing %s should return error", p.name)
		}
	}
}

// TestOrgansID verifies the custom ID reaches the Effector via Action.CellID.
func TestOrgansID(t *testing.T) {
	var gotCellID string
	a, err := testNew(testOrgans(meowire.Organs{
		ID: "wired",
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}}}, nil
		}},
		Act: &fnEffector{fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			gotCellID = a.CellID
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "work") {
	}
	if gotCellID != "wired" {
		t.Fatalf("CellID = %q, want %q", gotCellID, "wired")
	}
}

// TestDefaultID verifies an empty ID defaults to "agent".
func TestDefaultID(t *testing.T) {
	var gotCellID string
	a, err := testNew(testOrgans(meowire.Organs{
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}}}, nil
		}},
		Act: &fnEffector{fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			gotCellID = a.CellID
			return &meowire.Effect{Result: "ok"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for range a.Stimulate(context.Background(), "work") {
	}
	if gotCellID != "agent" {
		t.Fatalf("CellID = %q, want default %q", gotCellID, "agent")
	}
}

// TestFullOrgansWiring verifies Sandbox/Budget/Identity/Methods/Context reach the loop.
func TestFullOrgansWiring(t *testing.T) {
	var (
		sandboxCalled bool
		trimmerCalled bool
		gotIdentity   string
		gotMethods    []meowire.MethodSpec
		firstCtx      []string
	)
	calls := 0
	a, err := testNew(meowire.Organs{
		ID: "wired-agent",
		Think: &fnThinker{fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			gotIdentity = p.Identity
			gotMethods = p.Methods
			if calls == 0 {
				firstCtx = p.Context
			}
			calls++
			if calls == 1 {
				return &meowire.Decision{Text: "t", ToolCalls: []meowire.ToolCall{{ID: "x", Name: "tool"}}}, nil
			}
			return &meowire.Decision{Text: "ok"}, nil
		}},
		Act: &fnEffector{fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Result: "ok"}, nil
		}},
		Closer: &closerStub{},
		Hooks:  &meowire.Hooks{},
		Sandbox: sandboxFn{fn: func(ctx context.Context, a meowire.Action) (bool, string, error) {
			sandboxCalled = true
			return true, "", nil
		}},
		Budget: &meowire.ContextBudget{
			MaxTokens: 10,
			Trimmer: func(ctx []string, max int) []string {
				trimmerCalled = true
				return ctx
			},
		},
		System:   "sys",
		Methods:  []meowire.MethodSpec{{Name: "m1", Desc: "md"}},
		Tools:    []meowire.ToolSpec{{Name: "t1", Desc: "d"}},
		Context:  []string{"ctx1"},
		Identity: "wire",
	}, meowire.Config{})
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
	if gotIdentity != "wire" {
		t.Fatalf("Identity = %q, want %q", gotIdentity, "wire")
	}
	if len(gotMethods) != 1 || gotMethods[0].Name != "m1" {
		t.Fatalf("Methods = %+v, want [m1]", gotMethods)
	}
	if len(firstCtx) != 1 || firstCtx[0] != "ctx1" {
		t.Fatalf("first Think Context = %v, want [ctx1]", firstCtx)
	}
}
