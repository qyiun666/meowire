// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wire.go — wiring blueprint as a graph: nodes are the data objects the
// framework exposes (Prompt, Context, Decision, ...), edges are the slots
// (port / hook / built-in) that read or mutate a node. The blueprint is
// static and never changes at runtime; a host assembly is checked against it
// by the api package (WiringDiagram / Validate) at composition time.
package nerve

// WireCategory classifies a slot's role in the sense → decide → act flow.
type WireCategory string

const (
	CategorySense  WireCategory = "sense"  // perception input
	CategoryDecide WireCategory = "decide" // decision processing
	CategoryAct    WireCategory = "act"    // action output
)

// WireSemantics describes how a slot mutates its target.
type WireSemantics string

const (
	SemReplace   WireSemantics = "replace"   // overwrite target wholesale
	SemAppend    WireSemantics = "append"    // append to target
	SemTrim      WireSemantics = "trim"      // shrink target
	SemGate      WireSemantics = "gate"      // gate the flow (allow/deny)
	SemRead      WireSemantics = "read-only" // observe only, no mutation
	SemAct       WireSemantics = "act"       // perform an external action
	SemContainer WireSemantics = "container" // holds sub-slots, mutates nothing
)

// WireNode is a graph node: one data object in the sense → decide → act
// flow. Nodes are referenced by WirePoint.TargetID; ConnectomeNodes is the
// canonical list (an edge whose TargetID is not a node fails validation).
type WireNode struct {
	ID   string // stable id, referenced by WirePoint.TargetID
	Name string
	Desc string
}

// ConnectomeNodes returns the graph nodes (data objects). Fresh slice per
// call; callers must not rely on identity between calls.
func ConnectomeNodes() []WireNode {
	return []WireNode{
		{ID: "prompt", Name: "Prompt", Desc: "full prompt bundle: System+Identity+Methods+Tools+Context+Input+Plan+ToolResults"},
		{ID: "context", Name: "Context", Desc: "conversation context list (host base + sandbox denials)"},
		{ID: "toolresults", Name: "ToolResults", Desc: "structured tool feedback list (single track)"},
		{ID: "plan", Name: "Plan", Desc: "host-side plan text"},
		{ID: "bounds", Name: "Bounds", Desc: "execution boundary snapshot"},
		{ID: "decision", Name: "Decision", Desc: "Thinker output"},
		{ID: "action", Name: "Action", Desc: "tool action to execute"},
		{ID: "effect", Name: "Effect", Desc: "tool execution result"},
		{ID: "err", Name: "Error", Desc: "error value"},
		{ID: "output", Name: "Output", Desc: "final output of a Stimulate"},
		{ID: "timing", Name: "LoopTiming", Desc: "loop schedule (gap points)"},
		{ID: "resources", Name: "Resources", Desc: "external resources held by the agent"},
		{ID: "hooks", Name: "Hooks", Desc: "callback container (H1–H8 sub-slots)"},
	}
}

// WirePoint is one wiring slot — an edge in the wiring graph.
// Phase: 1 = framework-defined (always active), 2 = host-implemented port,
// 3 = host runtime update (hook function).
// TargetID: graph node this slot reads or mutates (see ConnectomeNodes);
// Target is the human-readable rendering of that edge.
// Required: New rejects a missing required slot; optional slots are not
// re-checked (P5b is implied by P5, G1 is api-injected).
type WirePoint struct {
	ID        string
	Name      string
	Phase     int
	Category  WireCategory
	TargetID  string // graph node id (ConnectomeNodes)
	Target    string // human-readable edge description
	Semantics WireSemantics
	// Slot is the runtime-replaceable name of this port ("" = not swappable);
	// it is the single source of truth for the string a host passes to
	// Agent.Replace. A slot implied by a parent (P5b) carries the parent's
	// name, because swapping the parent swaps the whole port.
	Slot     string
	Parallel bool // host may parallelize; the loop itself stays serial
	Required bool
	Desc     string
}

// SwappableSlots returns the slot names Agent.Replace accepts, in blueprint
// order, deduplicated. Each call returns a fresh slice.
func SwappableSlots() []string {
	seen := make(map[string]bool)
	var out []string
	for _, wp := range Connectome() {
		if wp.Slot == "" || seen[wp.Slot] {
			continue
		}
		seen[wp.Slot] = true
		out = append(out, wp.Slot)
	}
	return out
}

// Connectome returns the static wiring blueprint. Each call returns a fresh
// slice; callers must not rely on identity between calls.
func Connectome() []WirePoint {
	return []WirePoint{
		// --- ports (phase 2: host-implemented, all required) ---
		{ID: "P1", Name: "Think", Phase: 2, Category: CategoryDecide,
			TargetID: "prompt", Target: "Prompt (all fields) → Decision", Semantics: SemRead,
			Parallel: true, Required: true, Slot: "think",
			Desc: "LLM reasoning port; host may run parallel candidates"},
		{ID: "P2", Name: "Act", Phase: 2, Category: CategoryAct,
			TargetID: "action", Target: "Action → Effect", Semantics: SemAct,
			Parallel: true, Required: true, Slot: "act",
			Desc: "tool execution port; host may run tools in parallel"},
		{ID: "P3", Name: "Closer", Phase: 2, Category: CategoryAct,
			TargetID: "resources", Target: "external resources", Semantics: SemAct,
			Parallel: false, Required: true,
			Desc: "cleanup port, called once by Agent.Close; never swappable (resource binding)"},
		{ID: "P4", Name: "Hooks", Phase: 2, Category: CategoryDecide,
			TargetID: "hooks", Target: "H1–H8 sub-slots", Semantics: SemContainer,
			Parallel: false, Required: true, Slot: "hooks",
			Desc: "callback container; inner functions are optional"},
		{ID: "P5", Name: "Sandbox", Phase: 2, Category: CategoryAct,
			TargetID: "action", Target: "Action", Semantics: SemGate,
			Parallel: false, Required: true, Slot: "sandbox",
			Desc: "security gate before every tool execution"},
		{ID: "P5b", Name: "Sandbox.Bounds", Phase: 2, Category: CategorySense,
			TargetID: "bounds", Target: "Prompt.Bounds", Semantics: SemRead,
			Parallel: false, Required: false, Slot: "sandbox",
			Desc: "execution boundary snapshot once per Stimulate; implied by P5"},
		{ID: "P6", Name: "Budget", Phase: 2, Category: CategoryDecide,
			TargetID: "context", Target: "Prompt.Context", Semantics: SemTrim,
			Parallel: false, Required: true, Slot: "budget",
			Desc: "text-track trimming before each Think"},
		{ID: "P6b", Name: "Budget.TrimResults", Phase: 2, Category: CategoryDecide,
			TargetID: "toolresults", Target: "Prompt.ToolResults", Semantics: SemTrim,
			Parallel: false, Required: false, Slot: "budget",
			Desc: "structured-feedback trimming at the same checkpoint and limit; implied by P6"},

		// --- hooks (phase 3: host runtime updates, all required) ---
		{ID: "H1", Name: "BeforeStimulate", Phase: 3, Category: CategorySense,
			TargetID: "prompt", Target: "System, Identity, Methods, Tools, Context, Input, Plan", Semantics: SemReplace,
			Parallel: false, Required: true,
			Desc: "whole-field write-back applies to every round"},
		{ID: "H2", Name: "AfterStimulate", Phase: 3, Category: CategorySense,
			TargetID: "output", Target: "final output", Semantics: SemRead,
			Parallel: false, Required: true,
			Desc: "runs exactly once per Stimulate on all exit paths"},
		{ID: "H3", Name: "BeforeThink", Phase: 3, Category: CategorySense,
			TargetID: "context", Target: "Prompt.Context (whole-field)", Semantics: SemReplace,
			Parallel: false, Required: true,
			Desc: "memory retrieval injection point; replace p.Context wholesale"},
		{ID: "H4", Name: "AfterThink", Phase: 3, Category: CategoryDecide,
			TargetID: "decision", Target: "Decision", Semantics: SemRead,
			Parallel: false, Required: true,
			Desc: "observe decision; host updates Plan externally for H3 to inject next round"},
		{ID: "H5", Name: "BeforeAct", Phase: 3, Category: CategoryAct,
			TargetID: "action", Target: "Action", Semantics: SemReplace,
			Parallel: false, Required: true,
			Desc: "may mutate the action before execution"},
		{ID: "H6", Name: "AfterAct", Phase: 3, Category: CategoryAct,
			TargetID: "effect", Target: "Effect, err", Semantics: SemRead,
			Parallel: false, Required: true,
			Desc: "tool feedback observer"},
		{ID: "H7", Name: "OnError", Phase: 3, Category: CategoryDecide,
			TargetID: "err", Target: "err", Semantics: SemRead,
			Parallel: false, Required: true,
			Desc: "error observer (state=error already yielded)"},
		{ID: "H8", Name: "OnCycleEnd", Phase: 3, Category: CategoryDecide,
			TargetID: "output", Target: "final output", Semantics: SemRead,
			Parallel: false, Required: true,
			Desc: "exactly once per Cycle on normal, error and early-stop paths"},

		// --- framework built-ins (phase 1: always active, not host slots) ---
		{ID: "F1", Name: "ToolResults", Phase: 1, Category: CategorySense,
			TargetID: "toolresults", Target: "ToolResults", Semantics: SemAppend,
			Parallel: false, Required: true,
			Desc: "framework built-in: structured tool results appended each round (single feedback track)"},
		{ID: "G1", Name: "PauseGate", Phase: 1, Category: CategoryDecide,
			TargetID: "timing", Target: "loop timing", Semantics: SemGate,
			Parallel: false, Required: false,
			Desc: "api-injected automatically; honors Pause at gap points"},
	}
}
