// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring.go — assembly blueprint inspection: blueprint × assembly comparison.
//
// The blueprint is a graph: ConnectomeNodes (nerve) are the data-object
// nodes, Connectome (nerve) is the edge list (slots). This file extracts the
// host's actual assembly (WiringDiagram / BuildGraph), compares it against
// the blueprint (Validate) and renders the graph as ASCII (RenderDiagram).
// New rejects error-level findings (missing/incomplete required ports);
// info findings are surfaced here for hosts that want to inspect defaults.
package meowire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Slot is a blueprint entry plus its actual filled state in a host assembly.
type Slot struct {
	Wire   nerve.WirePoint
	Filled bool
}

// IssueLevel classifies a Validate finding.
type IssueLevel string

const (
	LevelError IssueLevel = "error" // required slot missing or incomplete (New rejects)
	LevelInfo  IssueLevel = "info"  // notable default, not a problem
)

// Issue is a single wiring finding from Validate.
type Issue struct {
	ID    string // "assembly" for assembly-level findings, else wire id
	Level IssueLevel
	Wire  string // wire id ("" for assembly-level findings)
	Msg   string
}

// WiringGraph is the assembled wiring graph: data-object nodes plus the
// slots (edges) with their actual filled state. Built by BuildGraph.
type WiringGraph struct {
	Nodes []WireNode
	Slots []Slot
}

// Connectome returns the static wiring blueprint: every slot edge the
// framework exposes (fresh slice per call).
func Connectome() []WirePoint {
	return nerve.Connectome()
}

// ConnectomeNodes returns the static wiring graph nodes: the data objects
// slots read or mutate (fresh slice per call).
func ConnectomeNodes() []WireNode {
	return nerve.ConnectomeNodes()
}

// WiringDiagram returns the filled state of every blueprint slot.
// Framework built-ins (F1 tool feedback, G1 PauseGate) always read as filled.
func WiringDiagram(o Organs) []Slot {
	bp := nerve.Connectome()
	slots := make([]Slot, 0, len(bp))
	for _, wp := range bp {
		slots = append(slots, Slot{Wire: wp, Filled: slotFilled(o, wp.ID)})
	}
	return slots
}

// BuildGraph assembles the wiring graph for a host assembly: nodes are the
// canonical data objects, slots are the edges (with filled state).
func BuildGraph(o Organs) WiringGraph {
	return WiringGraph{
		Nodes: nerve.ConnectomeNodes(),
		Slots: WiringDiagram(o),
	}
}

// SlotsByTarget returns every slot that reads or mutates the given graph
// node (TargetID), with filled state. This is the "find by function" query:
// e.g. who touches Context → P6 (trim), H1/H3 (replace), F1 (append).
func SlotsByTarget(o Organs, targetID string) []Slot {
	var out []Slot
	for _, s := range WiringDiagram(o) {
		if s.Wire.TargetID == targetID {
			out = append(out, s)
		}
	}
	return out
}

// slotFilled reports whether a blueprint slot is actually wired in the
// assembly. Ports check their Organs field; hooks check the Hooks container
// plus the specific callback; built-ins are always active.
func slotFilled(o Organs, id string) bool {
	switch id {
	case "P1":
		return o.Think != nil
	case "P2":
		return o.Act != nil
	case "P3":
		return o.Closer != nil
	case "P4":
		return o.Hooks != nil
	case "P5", "P5b":
		return o.Sandbox != nil
	case "P6":
		return o.Budget != nil
	case "H1":
		return o.Hooks != nil && o.Hooks.BeforeStimulate != nil
	case "H2":
		return o.Hooks != nil && o.Hooks.AfterStimulate != nil
	case "H3":
		return o.Hooks != nil && o.Hooks.BeforeThink != nil
	case "H4":
		return o.Hooks != nil && o.Hooks.AfterThink != nil
	case "H5":
		return o.Hooks != nil && o.Hooks.BeforeAct != nil
	case "H6":
		return o.Hooks != nil && o.Hooks.AfterAct != nil
	case "H7":
		return o.Hooks != nil && o.Hooks.OnError != nil
	case "H8":
		return o.Hooks != nil && o.Hooks.OnCycleEnd != nil
	case "F1", "G1":
		return true // framework built-in / api-injected
	default:
		return false
	}
}

// Validate compares a host assembly against the wiring blueprint and returns
// all findings:
//
//   - error: a required slot is not wired, or a wired port is incomplete
//     (ContextBudget without a Trimmer / MaxTokens — a budget that does not
//     trim is not a budget). New rejects these.
//   - info: notable defaults (empty Identity/Tools/Context, default rounds).
//
// Hosts call Validate at assembly/test time to surface info findings; New
// rejects error-level findings. There is no warn level: every wiring point
// is required (hooks H1–H8, Sandbox, Budget), so a missing point is a
// missing organ — an error, not a warning.
func Validate(o Organs, cfg Config) []Issue {
	var issues []Issue
	slots := WiringDiagram(o)

	// error: required slots missing (P5b is implied by P5 and not re-checked).
	for _, s := range slots {
		if s.Wire.ID == "P5b" {
			continue
		}
		if s.Wire.Required && !s.Filled {
			issues = append(issues, Issue{
				ID:    "assembly",
				Level: LevelError,
				Wire:  s.Wire.ID,
				Msg:   fmt.Sprintf("required port %s not injected", s.Wire.Name),
			})
		}
	}

	// error: Budget present but incomplete — a trimmer is the organ's function,
	// MaxTokens its limit; either missing means the port cannot regulate.
	if o.Budget != nil && (o.Budget.Trimmer == nil || o.Budget.MaxTokens <= 0) {
		issues = append(issues, Issue{
			ID:    "assembly",
			Level: LevelError,
			Wire:  "P6",
			Msg:   "ContextBudget must have a Trimmer and MaxTokens > 0 (a budget that does not trim is not a budget)",
		})
	}

	// info: notable defaults (zero-value Config semantics are documented).
	if o.Identity == "" {
		issues = append(issues, Issue{ID: "assembly", Level: LevelInfo, Wire: "P1", Msg: "Identity empty (agent has no persona)"})
	}
	if len(o.Tools) == 0 {
		issues = append(issues, Issue{ID: "assembly", Level: LevelInfo, Wire: "P2", Msg: "Tools empty (no tools declared)"})
	}
	if len(o.Context) == 0 {
		issues = append(issues, Issue{ID: "assembly", Level: LevelInfo, Wire: "P6", Msg: "Context empty"})
	}
	if cfg.MaxRounds <= 0 {
		issues = append(issues, Issue{ID: "assembly", Level: LevelInfo, Wire: "", Msg: "MaxRounds<=0 uses DefaultMaxRounds(8)"})
	}

	return issues
}

// RenderDiagram renders the assembly as an ASCII wiring graph: the data
// nodes first, then one line per slot (edge) marked [x] wired / [ ] unwired,
// with slot metadata and its target node.
func RenderDiagram(o Organs) string {
	var b strings.Builder
	b.WriteString("meowire wiring graph\n")

	b.WriteString("nodes:\n")
	for _, n := range nerve.ConnectomeNodes() {
		fmt.Fprintf(&b, "  %-9s %-12s %s\n", n.ID, n.Name, n.Desc)
	}

	b.WriteString("edges:\n")
	for _, s := range WiringDiagram(o) {
		mark := "[ ]"
		if s.Filled {
			mark = "[x]"
		}
		fmt.Fprintf(&b, "  %s %-5s %-16s %d %-7s %-10s -> %-8s %s\n",
			mark, s.Wire.ID, s.Wire.Name, s.Wire.Phase, s.Wire.Category,
			s.Wire.Semantics, s.Wire.TargetID, s.Wire.Desc)
	}
	return b.String()
}

// RenderJSON renders the assembled wiring graph as indented JSON: the
// canonical nodes plus every slot edge with its filled state. Machine
// readable counterpart of RenderDiagram — hosts persist it for observability
// dashboards, diff-based assembly review, or wiring documentation.
func RenderJSON(o Organs) ([]byte, error) {
	return json.MarshalIndent(BuildGraph(o), "", "  ")
}
