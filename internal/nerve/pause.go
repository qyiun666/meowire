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
	w := b.snapshot(ToolCall{}, remaining)
	lc.endWith(OutcomeSuspended) // a Resume will continue this cycle
	if !b.yield(Event{Kind: EventPaused, Wait: w}) {
		return false
	}
	return false
}

// snapshot freezes the loop state into a WaitInput handle — the single
// suspension primitive behind tool waits, sandbox asks and pauses alike.
// pending is the suspending tool (zero for a pause), remaining the calls after
// the suspension point.
func (b *actBatch) snapshot(pending ToolCall, remaining []ToolCall) *WaitInput {
	lc := b.lc
	sess := Session{
		round:       b.round,
		input:       lc.Input,
		plan:        lc.Plan,
		context:     slices.Clone(lc.Context),
		output:      b.out.String(),
		pending:     pending,
		remaining:   slices.Clone(remaining),
		toolResults: slices.Clone(lc.ToolResults),
	}
	return &WaitInput{CellID: lc.CellID, Call: pending, Session: sess}
}

// suspend ends the iterator on a wait: snapshot, StateWaiting, then the two
// wait events. sandboxAsk flavors the Session as a membrane confirmation
// (resolved through the tri-state protocol on Resume) rather than a
// tool-requested wait. Returns false when the consumer stopped.
func (b *actBatch) suspend(pending ToolCall, remaining []ToolCall, question string, sandboxAsk bool) (*WaitInput, bool) {
	lc := b.lc
	w := b.snapshot(pending, remaining)
	w.Question = question
	w.Session.sandboxAsk = sandboxAsk
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
