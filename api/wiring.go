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
// e.g. who touches Context → P6 (trim) and H3 (replace); the structured
// tool-result track is its own node (P6b, F1).
func SlotsByTarget(o Organs, targetID string) []Slot {
	var out []Slot
	for _, s := range WiringDiagram(o) {
		if s.Wire.TargetID == targetID {
			out = append(out, s)
		}
	}
	return out
}

// organFilled answers "is this blueprint slot wired?" per slot id. Ports read
// their own Organs field, hooks read the container plus the specific callback,
// and the framework built-in / api-injected slots are always active; the
// optional boot slot follows whether the host's Closer can boot. An unknown id
// is reported unfilled: a slot the api does not know about cannot be assumed
// present.
var organFilled = map[string]func(Organs) bool{
	"P1":  func(o Organs) bool { return o.Think != nil },
	"P2":  func(o Organs) bool { return o.Act != nil },
	"P3":  func(o Organs) bool { return o.Closer != nil },
	"P3b": func(o Organs) bool { _, bootable := o.Closer.(Bootable); return bootable },
	"P4":  func(o Organs) bool { return o.Hooks != nil },
	"P5":  func(o Organs) bool { return o.Sandbox != nil },
	"P5b": func(o Organs) bool { return o.Sandbox != nil },
	"P5c": func(o Organs) bool { return o.Sandbox != nil },
	"P6":  func(o Organs) bool { return o.Budget != nil },
	"P6b": func(o Organs) bool { return o.Budget != nil },
	"P7":  func(o Organs) bool { return o.Mem != nil },
	"P7b": func(o Organs) bool { return o.Mem != nil },
	"H1":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.BeforeStimulate != nil },
	"H2":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.AfterStimulate != nil },
	"H3":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.BeforeThink != nil },
	"H4":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.AfterThink != nil },
	"H5":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.BeforeAct != nil },
	"H6":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.AfterAct != nil },
	"H7":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.OnError != nil },
	"H8":  func(o Organs) bool { return o.Hooks != nil && o.Hooks.OnCycleEnd != nil },
	"F1":  func(Organs) bool { return true },
	"G1":  func(Organs) bool { return true },
}

// impliedSlots are sub-slots carried by a parent port (Sandbox.Bounds by
// Sandbox, Budget.TrimResults and Mem.Remember by their own port). They are
// reported by WiringDiagram for inspection but never checked twice.
var impliedSlots = map[string]bool{"P5b": true, "P5c": true, "P6b": true, "P7b": true}

// slotFilled reports whether a blueprint slot is actually wired in the
// assembly.
func slotFilled(o Organs, id string) bool {
	filled, ok := organFilled[id]
	return ok && filled(o)
}

// Validate compares a host assembly against the wiring blueprint and returns
// all findings:
//
//   - error: a required slot is not wired, a wired port is incomplete
//     (ContextBudget without a Trimmer / MaxTokens — a budget that does not
//     trim is not a budget), or the agent is unnamed (Organs.ID is what events
//     are attributed to and what a suspension handle is checked against). New
//     rejects these.
//   - info: notable defaults (empty Identity/Tools/Context, default rounds).
//
// Hosts call Validate at assembly/test time to surface info findings; New
// rejects error-level findings. There is no warn level: every wiring point
// is required (hooks H1–H8, Sandbox, Budget), so a missing point is a
// missing organ — an error, not a warning.
func Validate(o Organs, cfg Config) []Issue {
	var issues []Issue
	slots := WiringDiagram(o)

	// error: required slots missing. Implied sub-slots ride their parent port,
	// so they are not checked separately.
	for _, s := range slots {
		if impliedSlots[s.Wire.ID] {
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

	// error: Budget present but incomplete — a trimmer per growing track is the
	// organ's function and MaxTokens its limit; either missing means the budget
	// cannot regulate what reaches the brain.
	if o.Budget != nil && !nerve.CompleteBudget(o.Budget) {
		issues = append(issues, Issue{
			ID:    "assembly",
			Level: LevelError,
			Wire:  "P6",
			Msg:   "ContextBudget must have a Trimmer, a TrimResults and MaxTokens > 0 (a budget that does not trim both tracks is not a budget)",
		})
	}

	// error: an unnamed agent. ID is the identity every event carries and every
	// suspension handle is checked against; a default shared by all instances
	// would hand one agent's handle to another, so the name is required rather
	// than guessed.
	if o.ID == "" {
		issues = append(issues, Issue{
			ID:    "assembly",
			Level: LevelError,
			Msg:   "Organs.ID is empty (an agent must name itself; the ID is what events are attributed to and what a Session is checked against)",
		})
	}

	return append(issues, defaultNotes(o, cfg)...)
}

// defaultNotes reports the assembly choices that are legal but worth hearing
// about: an organ left empty or a limit left at its documented default.
func defaultNotes(o Organs, cfg Config) []Issue {
	var notes []Issue
	if o.Identity == "" {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "P1", Msg: "Identity empty (agent has no persona)"})
	}
	if len(o.Tools) == 0 {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "P2", Msg: "Tools empty (no tools declared)"})
	}
	if len(o.Context) == 0 {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "P6", Msg: "Context empty"})
	}
	if cfg.MaxRounds <= 0 {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "", Msg: "MaxRounds<=0 uses DefaultMaxRounds(8)"})
	}
	if cfg.ParallelActs {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "P2", Msg: "ParallelActs enabled (the Effector must be safe for concurrent Act calls)"})
	} else if cfg.MaxParallelActs > 0 {
		notes = append(notes, Issue{ID: "assembly", Level: LevelInfo, Wire: "P2", Msg: "MaxParallelActs set while ParallelActs is off (no batch runs concurrently, so the ceiling binds nothing)"})
	}
	return notes
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
