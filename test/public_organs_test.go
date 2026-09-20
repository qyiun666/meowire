// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// public_organs_test.go — the acceptance face of the kernel: every host port
// is implementable with public names alone, and the brain arrives as
// parameters, not as a port. internal/ is sealed by the compiler, so a
// contract type missing from api/ makes its port unimplementable outside this
// module — which shows up here as a compile error. The scripted endpoint
// (testutil.FakeBrain) stands in for the LLM the brain talks to; it is test
// scaffolding, not a host organ.
package meowire_test

import (
	"context"
	"slices"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// hostTouch records which organ ran, in order.
type hostTouch struct{ calls []string }

func (h *hostTouch) note(name string) { h.calls = append(h.calls, name) }

type hostEffector struct{ touch *hostTouch }

func (h *hostEffector) Act(_ context.Context, a meowire.Action) (*meowire.Effect, error) {
	h.touch.note("act:" + a.Call.Name)
	return &meowire.Effect{Result: "2"}, nil
}

type hostCloser struct{ touch *hostTouch }

func (h *hostCloser) Close() error {
	h.touch.note("close")
	return nil
}

type hostSandbox struct{ touch *hostTouch }

func (h *hostSandbox) Allow(_ context.Context, a meowire.Action) (meowire.Verdict, string, error) {
	h.touch.note("allow:" + a.Call.Name)
	return meowire.VerdictAllow, "", nil
}

func (h *hostSandbox) Emit(_ context.Context, u meowire.Utterance) (meowire.Verdict, string, error) {
	h.touch.note("emit")
	return meowire.VerdictAllow, "", nil
}

func (h *hostSandbox) Bounds() string { return "host bounds" }

type hostMemory struct{ touch *hostTouch }

func (h *hostMemory) Recall(_ context.Context, q meowire.MemoryQuery) ([]meowire.Record, error) {
	h.touch.note("recall")
	return []meowire.Record{{Key: "k", CellID: q.CellID, Kind: "note", Content: []byte("v")}}, nil
}

func (h *hostMemory) Remember(_ context.Context, f meowire.CycleFacts) error {
	h.touch.note("remember")
	return nil
}

// TestPublicSurfaceOrgansAssemble runs a two-round cycle on organs written
// exactly as an external host would write them (plus the brain parameters),
// every organ is consulted in loop order, and the cycle ends with the
// accumulated answer. The scripted endpoint notes "think" as each request
// arrives, so the brain's place in the order is still visible.
func TestPublicSurfaceOrgansAssemble(t *testing.T) {
	touch := &hostTouch{}
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{Text: "working", ToolCalls: []testutil.FakeCall{{ID: "c1", Name: "calc", Args: "1+1"}}},
		testutil.FakeCompletion{Text: "answer"},
	)
	fb.OnHit(func() { touch.note("think") })
	a, err := meowire.New(meowire.Blueprint{
		Organs: meowire.Organs{
			ID:      "public-host",
			Brain:   fb.Cfg(false),
			Act:     &hostEffector{touch: touch},
			Closer:  &hostCloser{touch: touch},
			Hooks:   meowire.FullHooks(meowire.Hooks{}),
			Sandbox: &hostSandbox{touch: touch},
			Mem:     &hostMemory{touch: touch},
			Budget: &meowire.ContextBudget{MaxTokens: 1000,
				Trimmer:     func(c []string, _ int) []string { return c },
				TrimResults: func(r []meowire.ToolResult, _ int) []meowire.ToolResult { return r },
			},
		},
	})
	if err != nil {
		t.Fatalf("New with public-surface organs: %v", err)
	}

	var done string
	emitRulings := 0
	for ev := range a.Stimulate(context.Background(), "solve 1+1") {
		switch ev.Kind {
		case meowire.EventError:
			t.Fatalf("error event with public organs: %v", ev.Err)
		case meowire.EventSandbox:
			if ev.Verdict.Call.Name == "" {
				emitRulings++
			}
		case meowire.EventDone:
			done = ev.Output
		}
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if done != "workinganswer" {
		t.Fatalf("done output = %q, want %q", done, "workinganswer")
	}
	if emitRulings != 2 {
		t.Fatalf("output membrane rulings = %d, want one per round", emitRulings)
	}
	want := []string{"recall", "think", "emit", "allow:calc", "act:calc", "recall", "think", "emit", "remember", "close"}
	if !slices.Equal(touch.calls, want) {
		t.Fatalf("organ calls = %v, want %v", touch.calls, want)
	}
}
