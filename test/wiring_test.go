// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_test.go — integration tests: wiring blueprint inspection through the
// public facade (New behavior, WiringDiagram, Validate, RenderDiagram).
package meowire_test

import (
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// TestNewAggregatesMissingPorts: multiple missing required ports are joined
// into one error naming every port (previous behavior returned only the
// first missing port).
func TestNewAggregatesMissingPorts(t *testing.T) {
	o := fullOrgans()
	o.Think = nil
	o.Act = nil
	_, err := meowire.New(meowire.Blueprint{Organs: o})
	if err == nil {
		t.Fatal("New with two missing ports should error")
	}
	for _, name := range []string{"Think", "Act"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q should mention %s", err.Error(), name)
		}
	}
}

// TestNewRejectsIncompleteHooks: a missing hook callback aborts New — every
// wiring point is required (explicit no-op, not absence).
func TestNewRejectsIncompleteHooks(t *testing.T) {
	o := testOrgans(meowire.Organs{})
	o.Hooks = fullHooks()
	o.Hooks.BeforeThink = nil
	if _, err := meowire.New(meowire.Blueprint{Organs: o}); err == nil {
		t.Fatal("New with a missing hook callback should error")
	}
}

// TestNewRejectsIncompleteBudget: a Budget without a Trimmer or MaxTokens
// aborts New — a budget that does not trim is not a budget.
func TestNewRejectsIncompleteBudget(t *testing.T) {
	o := testOrgans(meowire.Organs{})
	o.Budget = &meowire.ContextBudget{} // no trimmer, no MaxTokens
	if _, err := meowire.New(meowire.Blueprint{Organs: o}); err == nil {
		t.Fatal("New with an incomplete ContextBudget should error")
	}
}

// TestValidateSurfacesMissingHook: Validate flags every missing required
// hook callback as an error.
func TestValidateSurfacesMissingHook(t *testing.T) {
	o := testOrgans(meowire.Organs{})
	o.Hooks = fullHooks()
	o.Hooks.AfterThink = nil
	found := false
	for _, is := range meowire.Validate(o, meowire.Config{}) {
		if is.Level == meowire.LevelError && strings.Contains(is.Msg, "AfterThink") {
			found = true
		}
	}
	if !found {
		t.Error("Validate should error about the missing AfterThink callback")
	}
}

// TestWiringDiagramThroughFacade: the facade exposes the same blueprint
// slots; required ports of a full assembly are all filled.
func TestWiringDiagramThroughFacade(t *testing.T) {
	byID := map[string]meowire.Slot{}
	for _, s := range meowire.WiringDiagram(fullOrgans()) {
		byID[s.Wire.ID] = s
	}
	for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6", "F1", "G1"} {
		if !byID[id].Filled {
			t.Errorf("slot %s should be filled in a full assembly", id)
		}
	}
}

// TestRenderDiagramThroughFacade: the ASCII graph renders through the
// facade with headers, nodes and slot ids.
func TestRenderDiagramThroughFacade(t *testing.T) {
	out := meowire.RenderDiagram(fullOrgans())
	for _, want := range []string{"meowire wiring graph", "nodes:", "edges:", "P1", "H3", "[x]", "-> context"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagram missing %q", want)
		}
	}
}
