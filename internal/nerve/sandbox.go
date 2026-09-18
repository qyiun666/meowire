// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// sandbox.go — execution boundary: required port, the security membrane.
package nerve

import "context"

// Verdict is a tri-state sandbox ruling: Deny (never run), Allow (run now),
// Ask (suspend the loop and confirm externally before deciding). The zero
// value is Deny — an unset or default-constructed ruling never executes.
type Verdict int

const (
	VerdictDeny  Verdict = iota // fail-closed zero value: execution refused
	VerdictAllow                // permitted: proceed to execution
	VerdictAsk                  // suspended pending external confirmation
)

// verdictNames is the wire name of each ruling, indexed by value (same
// discipline as the event kind and loop state names).
var verdictNames = []string{"deny", "allow", "ask"}

// verdictName is the wire name of a membrane ruling; "" means the value is not
// a ruling this build knows, which is a reason to refuse a record, not to write
// one that can never be read back.
func verdictName(v Verdict) string { return nameOf(verdictNames, v) }

// verdictOfName resolves a wire name; an unknown ruling is rejected rather than
// read as the zero-value Deny.
func verdictOfName(name string) (Verdict, bool) {
	return valueOfName[Verdict](verdictNames, name)
}

// Sandbox is the security membrane (required port). It guards both sides of
// the loop: every tool execution passes Allow before it runs, and every
// utterance passes Emit before it reaches the consumer. The host implements
// security policies; the framework provides the interception point and audits
// each decision via EventSandbox. A ruling outside the three named states is a
// Deny as well — a value this build cannot name has not permitted anything.
// A VerdictAsk suspends the loop and the host resolves it through the same
// Resume channel as ask_user: a Response stating a Deny refuses the call (or the
// utterance), an answering Response without one grants it. Bounds() declares the
// execution boundary, surfaced to the Thinker via Prompt.Bounds once per
// Stimulate.
type Sandbox interface {
	Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
	// Emit rules on one utterance before the framework writes it to the event
	// stream: Allow says it as generated, Deny replaces it with
	// `[sandbox-denied: reason]`, Ask withholds it and suspends (Reason is the
	// question text then). A non-nil error is a fail-closed Deny, as in Allow.
	Emit(ctx context.Context, u Utterance) (verdict Verdict, reason string, err error)
	// Bounds returns the execution boundary description (host defined),
	// surfaced to the Thinker via Prompt.Bounds once per Stimulate.
	Bounds() string
}

// Utterance is what the output membrane is asked to rule on: one round's
// generated text, identified by the cell and round that produced it.
type Utterance struct {
	CellID string
	Round  int
	Text   string
}
