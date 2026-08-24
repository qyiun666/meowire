// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_test.go — integration tests: wiring blueprint inspection through the
// public facade (New behavior, WiringDiagram, Validate, RenderDiagram).
package meowire_test

import (
	"context"
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

// TestNewAllowsHalfWiredHooks: warn-level findings (half-wired hook pairs)
// never block New by default — only error-level findings do.
func TestNewAllowsHalfWiredHooks(t *testing.T) {
	o := testOrgans(meowire.Organs{
		Hooks: &meowire.Hooks{
			BeforeThink: func(ctx context.Context, p *meowire.Prompt) error { return nil },
		},
	})
	if _, err := meowire.New(meowire.Blueprint{Organs: o}); err != nil {
		t.Fatalf("New with half-wired hooks should succeed: %v", err)
	}
}

// TestNewStrictBlocksWarns: Blueprint.Strict promotes warn-level findings
// (half-wired hook pair) to New-blocking errors; the same assembly passes
// without Strict.
func TestNewStrictBlocksWarns(t *testing.T) {
	o := testOrgans(meowire.Organs{
		Hooks: &meowire.Hooks{
			BeforeThink: func(ctx context.Context, p *meowire.Prompt) error { return nil },
		},
	})
	if _, err := meowire.New(meowire.Blueprint{Organs: o, Strict: true}); err == nil {
		t.Fatal("Strict New with half-wired hooks should error")
	}
	if _, err := meowire.New(meowire.Blueprint{Organs: o}); err != nil {
		t.Fatalf("non-strict New should succeed: %v", err)
	}
}

// TestValidateSurfacesHalfWiredPair: the same assembly that New accepts is
// flagged by Validate as warn — hosts use Validate for strict assembly checks.
func TestValidateSurfacesHalfWiredPair(t *testing.T) {
	o := testOrgans(meowire.Organs{
		Hooks: &meowire.Hooks{
			BeforeThink: func(ctx context.Context, p *meowire.Prompt) error { return nil },
		},
	})
	found := false
	for _, is := range meowire.Validate(o, meowire.Config{}) {
		if is.Level == meowire.LevelWarn && strings.Contains(is.Msg, "AfterThink") {
			found = true
		}
	}
	if !found {
		t.Error("Validate should warn about the missing AfterThink counterpart")
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
