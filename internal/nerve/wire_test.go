// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wire_test.go — Connectome blueprint integrity tests.
package nerve

import "testing"

// TestConnectomeUniqueIDs verifies every blueprint slot has a unique ID.
func TestConnectomeUniqueIDs(t *testing.T) {
	bp := Connectome()
	seen := make(map[string]bool, len(bp))
	for _, wp := range bp {
		if seen[wp.ID] {
			t.Fatalf("duplicate wire id %q", wp.ID)
		}
		seen[wp.ID] = true
	}
}

// TestConnectomeNodesUnique verifies every graph node has a unique ID.
func TestConnectomeNodesUnique(t *testing.T) {
	nodes := ConnectomeNodes()
	seen := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if seen[n.ID] {
			t.Fatalf("duplicate node id %q", n.ID)
		}
		if n.ID == "" {
			t.Fatal("node with empty id")
		}
		seen[n.ID] = true
	}
}

// TestEveryTargetIDResolves verifies every slot edge points at a node that
// exists in ConnectomeNodes — the graph stays closed under its own nodes.
func TestEveryTargetIDResolves(t *testing.T) {
	nodeIDs := make(map[string]bool)
	for _, n := range ConnectomeNodes() {
		nodeIDs[n.ID] = true
	}
	for _, wp := range Connectome() {
		if !nodeIDs[wp.TargetID] {
			t.Errorf("wire %s targets unknown node %q", wp.ID, wp.TargetID)
		}
	}
}

// TestConnectomeCoversAllSlots verifies the blueprint describes every
// framework slot: six required ports, eight hooks, two built-ins.
func TestConnectomeCoversAllSlots(t *testing.T) {
	bp := Connectome()
	byID := make(map[string]WirePoint, len(bp))
	for _, wp := range bp {
		byID[wp.ID] = wp
	}
	want := []string{
		"P1", "P2", "P3", "P4", "P5", "P5b", "P6", "P6b", "P7", "P7b", // ports
		"H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8", // hooks
		"F1", "G1", // framework built-ins
	}
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Fatalf("blueprint missing slot %q", id)
		}
	}
}

// TestConnectomeRequiredPorts verifies every wiring point is required: the
// seven Organs ports, the eight hooks (all required since 1.2.0 — explicit
// no-op, not absence) and the framework-built-in F1. Implied sub-slots ride
// their parent port.
func TestConnectomeRequiredPorts(t *testing.T) {
	required := map[string]bool{
		"P1": true, "P2": true, "P3": true, "P4": true, "P5": true, "P6": true, "P7": true,
		"H1": true, "H2": true, "H3": true, "H4": true, "H5": true, "H6": true, "H7": true, "H8": true,
		"F1": true, // built-in, always active
	}
	for _, wp := range Connectome() {
		if wp.Required != required[wp.ID] {
			t.Errorf("wire %s Required = %v, want %v", wp.ID, wp.Required, required[wp.ID])
		}
	}
}

// TestConnectomeSemanticsValid verifies every slot carries a known semantics
// and a phase of 1–3.
func TestConnectomeSemanticsValid(t *testing.T) {
	sems := map[WireSemantics]bool{
		SemReplace: true, SemAppend: true, SemTrim: true,
		SemGate: true, SemRead: true, SemAct: true, SemContainer: true,
	}
	for _, wp := range Connectome() {
		if !sems[wp.Semantics] {
			t.Errorf("wire %s has unknown semantics %q", wp.ID, wp.Semantics)
		}
		if wp.Phase < 1 || wp.Phase > 3 {
			t.Errorf("wire %s has invalid phase %d", wp.ID, wp.Phase)
		}
	}
}

// TestConnectomeNodeCoverage verifies the node set covers every data object
// the slots describe (a slot added with a new TargetID forces a node).
func TestConnectomeNodeCoverage(t *testing.T) {
	want := []string{
		"prompt", "context", "plan", "bounds", "decision", "action",
		"effect", "err", "output", "timing", "resources", "hooks",
	}
	nodeIDs := make(map[string]bool)
	for _, n := range ConnectomeNodes() {
		nodeIDs[n.ID] = true
	}
	for _, id := range want {
		if !nodeIDs[id] {
			t.Errorf("node set missing %q", id)
		}
	}
}
