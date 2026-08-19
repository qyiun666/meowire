// meow_test.go — root package tests: New/Stimulate/Close flow, event consumption.
package meowire

import (
	"context"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// allowSandbox permits all tool calls (test-only Sandbox).
type allowSandbox struct{}

func (allowSandbox) Allow(ctx context.Context, a nerve.Action) (bool, string, error) {
	return true, "", nil
}

// sandboxFn is a test-only Sandbox stub driven by a function field.
type sandboxFn struct {
	fn func(ctx context.Context, a nerve.Action) (bool, string, error)
}

func (s sandboxFn) Allow(ctx context.Context, a nerve.Action) (bool, string, error) {
	return s.fn(ctx, a)
}

// fullOrgans returns an Organs with all six required ports wired (test-only).
func fullOrgans() Organs {
	return Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "ok"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
		Closer:  &closerStub{},
		Hooks:   &nerve.Hooks{},
		Sandbox: allowSandbox{},
		Budget:  &nerve.ContextBudget{MaxTokens: 100, Trimmer: func(ctx []string, max int) []string { return ctx }},
	}
}

// testOrgans returns fullOrgans with non-zero overrides applied (test-only).
func testOrgans(o Organs) Organs {
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
	base.Tools = o.Tools
	base.Context = o.Context
	base.Identity = o.Identity
	return base
}

// TestNewAndStimulate verifies creating an Agent and consuming events.
func TestNewAndStimulate(t *testing.T) {
	a, err := New(testOrgans(Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "meow-answer"}, nil
		}},
	}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var events []nerve.Event
	for ev := range a.Stimulate(context.Background(), "work") {
		events = append(events, ev)
	}

	// Expect: State(thinking), Text, State(done), Done
	if len(events) != 4 {
		t.Fatalf("events count = %d, want 4", len(events))
	}
	if events[1].Kind != nerve.EventText || events[1].Text != "meow-answer" {
		t.Fatalf("events[1] = %+v, want EventText(meow-answer)", events[1])
	}
	if events[3].Kind != nerve.EventDone || events[3].Output != "meow-answer" {
		t.Fatalf("events[3] = %+v, want EventDone(meow-answer)", events[3])
	}

	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestCloseIdempotent verifies Close can be called multiple times without error.
func TestCloseIdempotent(t *testing.T) {
	a, err := New(testOrgans(Organs{}), Config{})
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
		set  func(o *Organs)
	}{
		{"Think", func(o *Organs) { o.Think = nil }},
		{"Act", func(o *Organs) { o.Act = nil }},
		{"Closer", func(o *Organs) { o.Closer = nil }},
		{"Hooks", func(o *Organs) { o.Hooks = nil }},
		{"Sandbox", func(o *Organs) { o.Sandbox = nil }},
		{"Budget", func(o *Organs) { o.Budget = nil }},
	}
	for _, p := range ports {
		o := fullOrgans()
		p.set(&o)
		if _, err := New(o, Config{}); err == nil {
			t.Fatalf("New with missing %s should return error", p.name)
		}
	}
}

// TestOrgansID verifies the custom ID reaches the Effector via Action.CellID.
func TestOrgansID(t *testing.T) {
	var gotCellID string
	a, err := New(testOrgans(Organs{
		ID: "wired",
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "t", ToolCalls: []nerve.ToolCall{{ID: "x", Name: "tool"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			gotCellID = a.CellID
			return &nerve.Effect{Result: "ok"}, nil
		}},
	}), Config{})
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
	a, err := New(testOrgans(Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			return &nerve.Decision{Text: "t", ToolCalls: []nerve.ToolCall{{ID: "x", Name: "tool"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			gotCellID = a.CellID
			return &nerve.Effect{Result: "ok"}, nil
		}},
	}), Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for range a.Stimulate(context.Background(), "work") {
	}
	if gotCellID != "agent" {
		t.Fatalf("CellID = %q, want default %q", gotCellID, "agent")
	}
}

// TestFullOrgansWiring verifies Sandbox/Budget/Identity/Context reach the loop.
func TestFullOrgansWiring(t *testing.T) {
	var (
		sandboxCalled bool
		trimmerCalled bool
		gotIdentity   nerve.Identity
		firstCtx      []string
	)
	calls := 0
	a, err := New(Organs{
		ID: "wired-agent",
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
			gotIdentity = p.Identity
			if calls == 0 {
				firstCtx = p.Context // capture the injected context before feedback accumulates
			}
			calls++
			if calls == 1 {
				return &nerve.Decision{Text: "t", ToolCalls: []nerve.ToolCall{{ID: "x", Name: "tool"}}}, nil
			}
			return &nerve.Decision{Text: "ok"}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
			return &nerve.Effect{Result: "ok"}, nil
		}},
		Closer: &closerStub{},
		Hooks:  &nerve.Hooks{},
		Sandbox: sandboxFn{fn: func(ctx context.Context, a nerve.Action) (bool, string, error) {
			sandboxCalled = true
			return true, "", nil
		}},
		Budget: &nerve.ContextBudget{
			MaxTokens: 10,
			Trimmer: func(ctx []string, max int) []string {
				trimmerCalled = true
				return ctx
			},
		},
		System:   "sys",
		Tools:    []nerve.ToolSpec{{Name: "t1", Desc: "d"}},
		Context:  []string{"ctx1"},
		Identity: nerve.Identity{Name: "wire", Role: "test"},
	}, Config{})
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
	if gotIdentity.Name != "wire" {
		t.Fatalf("Identity = %+v, want Name=wire", gotIdentity)
	}
	if len(firstCtx) != 1 || firstCtx[0] != "ctx1" {
		t.Fatalf("first Think Context = %v, want [ctx1]", firstCtx)
	}
}
