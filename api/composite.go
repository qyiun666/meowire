// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// composite.go — unified graph view: the static assembly subgraph (internal
// data-object nodes + slot edges) merged with the dynamic synapse graph
// (external agent nodes + synaptic edges with weights). One graph, two
// sources: the blueprint × assembly projection (BuildGraph) and the live
// synapse snapshot (Synapse.Edges). View is unified, data stays separate.
package meowire

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// CompositeGraph is the unified wiring view of an agent colony: internal
// data-object nodes with their slot edges (static, from the assembly) plus
// external agent nodes with their synaptic edges (dynamic, from the
// synapse graph).
type CompositeGraph struct {
	Nodes    []WireNode // internal data-object nodes (ConnectomeNodes)
	Slots    []Slot     // internal slot edges with filled state
	Agents   []string   // external agent nodes (deduplicated, sorted)
	Synapses []Edge     // external synaptic edges (sorted, deep-copied)
	// Floor is the conduction threshold the graph reports — the weight below
	// which Fire refuses to carry a signal. 0 when the graph gates nothing
	// (or reports no threshold at all).
	Floor float64
}

// floorReporter is the optional capability of a synapse that gates delivery by
// weight; the reference Direct does, a host router need not.
type floorReporter interface{ Floor() float64 }

// BuildComposite assembles the composite graph: the static assembly
// subgraph plus a snapshot of the dynamic synapse graph. syn == nil renders
// the internal subgraph only. The synapse snapshot is sorted (from, to) so
// the output is deterministic; empty slices serialize as [] in JSON.
func BuildComposite(ctx context.Context, o Organs, syn Synapse) (CompositeGraph, error) {
	g := CompositeGraph{
		Nodes:    ConnectomeNodes(),
		Slots:    WiringDiagram(o),
		Agents:   []string{},
		Synapses: []Edge{},
	}
	if syn == nil {
		return g, nil
	}
	edges, err := syn.Edges(ctx, "")
	if err != nil {
		return CompositeGraph{}, fmt.Errorf("meowire.BuildComposite: %w", err)
	}
	slices.SortFunc(edges, func(a, b Edge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	g.Synapses = edges
	if r, ok := syn.(floorReporter); ok {
		g.Floor = r.Floor()
	}

	seen := make(map[string]struct{})
	for _, e := range edges {
		seen[e.From] = struct{}{}
		seen[e.To] = struct{}{}
	}
	g.Agents = slices.Sorted(maps.Keys(seen))
	return g, nil
}

// RenderComposite renders the composite graph as ASCII: the internal nodes
// and slot edges first, then the external agents and synaptic edges with
// weight and delivery count. An edge the graph would refuse to conduct (its
// weight sits below the floor that graph reports) is flagged; a graph that
// gates nothing flags nothing.
func RenderComposite(ctx context.Context, o Organs, syn Synapse) (string, error) {
	g, err := BuildComposite(ctx, o, syn)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("meowire composite graph\n")

	b.WriteString("internal nodes:\n")
	for _, n := range g.Nodes {
		fmt.Fprintf(&b, "  %-9s %-12s %s\n", n.ID, n.Name, n.Desc)
	}

	b.WriteString("internal edges:\n")
	for _, s := range g.Slots {
		mark := "[ ]"
		if s.Filled {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "  %s %-5s %-16s %d %-7s %-10s -> %-8s %s\n",
			mark, s.Wire.ID, s.Wire.Name, s.Wire.Phase, s.Wire.Category,
			s.Wire.Semantics, s.Wire.TargetID, s.Wire.Desc)
	}

	b.WriteString("external agents:\n")
	if len(g.Agents) == 0 {
		b.WriteString("  (none — synapse graph empty or not wired)\n")
	} else {
		b.WriteString("  " + strings.Join(g.Agents, "  ") + "\n")
	}

	b.WriteString("external synapses:\n")
	for _, e := range g.Synapses {
		flag := ""
		if g.Floor > 0 && e.Weight < g.Floor {
			flag = fmt.Sprintf("  ! below floor (%.3f)", g.Floor)
		}
		fmt.Fprintf(&b, "  %s -> %s  w=%.3f  fired=%d%s\n",
			e.From, e.To, e.Weight, e.Fired, flag)
	}
	return b.String(), nil
}

// RenderCompositeJSON renders the composite graph as indented JSON — the
// machine-readable counterpart of RenderComposite. Hosts persist it for
// colony-wide observability (one snapshot = internal wiring + synapse state).
func RenderCompositeJSON(ctx context.Context, o Organs, syn Synapse) ([]byte, error) {
	g, err := BuildComposite(ctx, o, syn)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(g, "", "  ")
}
