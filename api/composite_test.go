// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// composite_test.go — unified graph view tests: internal subgraph + synapse
// edges merged into one renderable graph.
package meowire

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// errorSynapse is a Synapse stub whose Edges always fails (error path test).
type errorSynapse struct{}

func (errorSynapse) Link(context.Context, string, string, float64) error {
	return errors.New("unused")
}
func (errorSynapse) Unlink(context.Context, string, string) error { return errors.New("unused") }
func (errorSynapse) Reinforce(context.Context, string, string, float64) error {
	return errors.New("unused")
}
func (errorSynapse) Fire(context.Context, Signal) error { return errors.New("unused") }
func (errorSynapse) Edges(context.Context, string) ([]Edge, error) {
	return nil, errors.New("synapse down")
}

// TestBuildCompositeInternalOnly: nil synapse yields the internal subgraph
// with no external agents or synapses.
func TestBuildCompositeInternalOnly(t *testing.T) {
	g, err := BuildComposite(context.Background(), fullOrgans(), nil)
	if err != nil {
		t.Fatalf("BuildComposite: %v", err)
	}
	if len(g.Nodes) == 0 || len(g.Slots) == 0 {
		t.Fatal("internal subgraph should carry nodes and slots")
	}
	if len(g.Agents) != 0 || len(g.Synapses) != 0 {
		t.Fatalf("no synapse → agents = %v, synapses = %v, want empty", g.Agents, g.Synapses)
	}
}

// TestBuildCompositeWithSynapse: synapse edges become sorted external
// synapses; agent ids are deduplicated and sorted.
func TestBuildCompositeWithSynapse(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{Initial: []Edge{
		{From: "b", To: "c", Weight: 0.9, Fired: 2},
		{From: "a", To: "b", Weight: 1.5, Fired: 3},
		{From: "a", To: "b", Weight: 0.1, Fired: 0}, // duplicate from->to: last wins
	}})
	g, err := BuildComposite(ctx, fullOrgans(), d)
	if err != nil {
		t.Fatalf("BuildComposite: %v", err)
	}
	want := []string{"a", "b", "c"}
	if strings.Join(g.Agents, ",") != strings.Join(want, ",") {
		t.Fatalf("agents = %v, want %v", g.Agents, want)
	}
	if len(g.Synapses) != 2 {
		t.Fatalf("synapses = %d, want 2 (deduplicated by from->to)", len(g.Synapses))
	}
	if g.Synapses[0].From != "a" || g.Synapses[0].To != "b" || g.Synapses[0].Weight != 0.1 {
		t.Fatalf("synapses[0] = %+v, want a->b w=0.1 (sorted, last link wins)", g.Synapses[0])
	}
	if g.Synapses[1].From != "b" || g.Synapses[1].To != "c" {
		t.Fatalf("synapses[1] = %+v, want b->c", g.Synapses[1])
	}
}

// TestBuildCompositeError: an Edges failure surfaces as a wrapped error.
func TestBuildCompositeError(t *testing.T) {
	if _, err := BuildComposite(context.Background(), fullOrgans(), errorSynapse{}); err == nil {
		t.Fatal("expected error from failing synapse")
	}
}

// TestRenderComposite: ASCII output carries both subgraphs, and flags exactly
// the edges the graph itself would refuse to conduct.
func TestRenderComposite(t *testing.T) {
	ctx := context.Background()
	initial := []Edge{
		{From: "a", To: "b", Weight: 1.5, Fired: 3},
		{From: "b", To: "c", Weight: 0.1, Fired: 0},
	}
	d := NewDirect(DirectConfig{Floor: 0.3, Initial: initial})
	out, err := RenderComposite(ctx, fullOrgans(), d)
	if err != nil {
		t.Fatalf("RenderComposite: %v", err)
	}
	for _, want := range []string{
		"meowire composite graph",
		"internal nodes:",
		"internal edges:",
		"external agents:",
		"a  b  c",
		"external synapses:",
		"a -> b  w=1.500  fired=3",
		"b -> c  w=0.100  fired=0  ! below floor (0.300)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	ungated, err := RenderComposite(ctx, fullOrgans(), NewDirect(DirectConfig{Initial: initial}))
	if err != nil {
		t.Fatalf("RenderComposite (no floor): %v", err)
	}
	if strings.Contains(ungated, "! below floor") {
		t.Error("a graph that gates nothing flagged an edge as weak")
	}
}

// TestRenderCompositeInternalOnly: with no synapse the external section
// still renders (stating the absence).
func TestRenderCompositeInternalOnly(t *testing.T) {
	out, err := RenderComposite(context.Background(), fullOrgans(), nil)
	if err != nil {
		t.Fatalf("RenderComposite: %v", err)
	}
	if !strings.Contains(out, "external agents:") {
		t.Fatal("external agents section missing")
	}
	if !strings.Contains(out, "none") {
		t.Fatal("expected explicit empty-agent note")
	}
	if strings.Contains(out, "w=") {
		t.Fatal("no synapses wired → no synapse lines expected")
	}
}

// TestRenderCompositeJSON: the machine-readable view is valid JSON carrying
// both subgraphs.
func TestRenderCompositeJSON(t *testing.T) {
	ctx := context.Background()
	d := NewDirect(DirectConfig{Initial: []Edge{{From: "a", To: "b", Weight: 1.5, Fired: 3}}})
	doc, err := RenderCompositeJSON(ctx, fullOrgans(), d)
	if err != nil {
		t.Fatalf("RenderCompositeJSON: %v", err)
	}
	if !json.Valid(doc) {
		t.Fatal("output is not valid JSON")
	}
	var g CompositeGraph
	if err := json.Unmarshal(doc, &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(g.Nodes) == 0 || len(g.Slots) == 0 {
		t.Fatal("internal subgraph should be present")
	}
	if len(g.Agents) != 2 || len(g.Synapses) != 1 {
		t.Fatalf("agents = %v, synapses = %v, want 2 agents / 1 synapse", g.Agents, g.Synapses)
	}
}
