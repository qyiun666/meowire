// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wire_test.go — Connectome blueprint integrity tests.
package nerve

import (
	"slices"
	"testing"
)

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
// framework slot: six required host ports, eight hooks, three built-ins.
func TestConnectomeCoversAllSlots(t *testing.T) {
	bp := Connectome()
	byID := make(map[string]WirePoint, len(bp))
	for _, wp := range bp {
		byID[wp.ID] = wp
	}
	want := []string{
		"P2", "P3", "P4", "P5", "P5b", "P5c", "P6", "P6b", "P7", "P7b", // ports
		"H1", "H2", "H3", "H4", "H5", "H6", "H7", "H8", // hooks
		"F1", "F2", "G1", // framework built-ins (F2 = the bundled brain)
	}
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Fatalf("blueprint missing slot %q", id)
		}
	}
}

// TestConnectomeRequiredPorts verifies every wiring point is required: the
// six Organs ports, the eight hooks (all required since 1.2.0 — explicit
// no-op, not absence) and the framework built-ins F1 and F2 (the brain is
// always wired; its parameters are checked by name). Implied sub-slots ride
// their parent port.
func TestConnectomeRequiredPorts(t *testing.T) {
	required := map[string]bool{
		"P2": true, "P3": true, "P4": true, "P5": true, "P6": true, "P7": true,
		"H1": true, "H2": true, "H3": true, "H4": true, "H5": true, "H6": true, "H7": true, "H8": true,
		"F1": true, // built-in, always active
		"F2": true, // the bundled brain, always wired
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

// TestConnectomeNodeCoverage pins the graph in both directions: every data
// object is touched by exactly the listed slots (in blueprint order), and the
// node set is exactly the objects the slots describe. A re-pointed edge, a slot
// added beside an existing one, a node nothing touches and a node declared but
// untargeted all fail here. The expected sets are written out rather than derived
// from Connectome — a guard that recomputes its own answer can never disagree.
func TestConnectomeNodeCoverage(t *testing.T) {
	want := map[string][]string{
		"prompt":      {"H1", "F2"},
		"context":     {"P6", "H3"},
		"toolresults": {"P6b", "F1"},
		"memories":    {"P7"},
		"plan":        nil, // host-side text: no slot reads or mutates it
		"bounds":      {"P5b"},
		"decision":    {"H4"},
		"action":      {"P2", "P5", "H5"},
		"effect":      {"H6"},
		"err":         {"H7"},
		"output":      {"P5c", "P7b", "H2", "H8"},
		"timing":      {"G1"},
		"resources":   {"P3", "P3b"},
		"hooks":       {"P4"},
	}

	touched := map[string][]string{}
	for _, wp := range Connectome() {
		touched[wp.TargetID] = append(touched[wp.TargetID], wp.ID)
	}
	for id, slots := range want {
		if !slices.Equal(touched[id], slots) {
			t.Errorf("node %q touched by %v, want %v", id, touched[id], slots)
		}
		delete(touched, id)
	}
	for id, slots := range touched {
		t.Errorf("slots %v target %q, which is not a declared node", slots, id)
	}

	nodes := map[string]bool{}
	for _, n := range ConnectomeNodes() {
		nodes[n.ID] = true
	}
	for id := range want {
		if !nodes[id] {
			t.Errorf("node set missing %q", id)
		}
	}
	for id := range nodes {
		if _, ok := want[id]; !ok {
			t.Errorf("node %q is declared but no slot targets it", id)
		}
	}
}
