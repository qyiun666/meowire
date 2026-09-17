// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// pause.go — the pause gate and the suspension snapshot both wait paths share.
package nerve

import (
	"slices"
)

// PauseGate is the pause gate (optional; nil = pause unsupported). The loop
// checks it at gap points (before each Think and before each tool execution);
// a pending pause yields EventState(StatePaused) + EventPaused with a Session
// snapshot and ends the iterator normally — the host resumes via
// Resume(sess, ""), the same channel a tool wait uses.
type PauseGate struct {
	// IsPaused reports whether a pause has been requested.
	IsPaused func() bool
}

// pause checks the gate at a gap point. When a pause is pending it yields
// EventState(StatePaused) + EventPaused with a snapshot carrying remaining
// (the calls not yet executed — nil at a Think gap, so Resume finishes them
// first) and ends the iterator normally: no Done, no Error. It returns false
// when the caller must return immediately — paused, or the consumer stopped.
func (b *actBatch) pause(remaining []ToolCall) bool {
	lc := b.lc
	if lc.Pause == nil || lc.Pause.IsPaused == nil || !lc.Pause.IsPaused() {
		return true
	}
	lc.State = StatePaused
	if !b.yield(Event{Kind: EventState, State: StatePaused}) {
		return false
	}
	w := b.snapshot(suspension{remaining: remaining, kind: waitPause})
	lc.endWith(OutcomeSuspended) // a Resume will continue this cycle
	if !b.yield(Event{Kind: EventPaused, Wait: w}) {
		return false
	}
	return false
}

// suspension describes one stop for external input: the call being waited on
// (zero for a pause or an utterance confirmation), the calls that still have
// to run, the question shown to the outside, why the loop stopped, and the
// text the output membrane withheld.
type suspension struct {
	pending   ToolCall
	remaining []ToolCall
	question  string
	kind      waitKind
	utterance string // withheld text (kind == waitUtterance)
}

// snapshot freezes the loop state into a WaitInput handle — the single
// suspension primitive behind tool waits, both membrane asks and pauses
// alike. The handle records which cell produced it: a Session is only valid
// against the organs that made it.
func (b *actBatch) snapshot(s suspension) *WaitInput {
	lc := b.lc
	sess := Session{
		round:       b.round,
		input:       lc.Input,
		plan:        lc.Plan,
		context:     slices.Clone(lc.Context),
		output:      b.out.String(),
		pending:     s.pending,
		remaining:   slices.Clone(s.remaining),
		toolResults: slices.Clone(lc.ToolResults),
		cell:        lc.CellID,
		kind:        s.kind,
		utterance:   s.utterance,
	}
	return &WaitInput{CellID: lc.CellID, Call: s.pending, Question: s.question, Session: sess}
}

// suspend ends the iterator on a wait: snapshot, StateWaiting, then the two
// wait events. Returns false when the consumer stopped.
func (b *actBatch) suspend(s suspension) (*WaitInput, bool) {
	lc := b.lc
	w := b.snapshot(s)
	lc.endWith(OutcomeSuspended) // a Resume will continue this cycle
	lc.State = StateWaiting
	if !b.yield(Event{Kind: EventState, State: StateWaiting}) {
		return w, false
	}
	if !b.yield(Event{Kind: EventWaitInput, Wait: w}) {
		return w, false
	}
	return w, true
}
