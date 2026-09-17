// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// inbox.go — the inbound side of a cell: the round's volatile track of what
// arrived, the requests among them that expect an answer, and the notice veto.
package nerve

import (
	"cmp"
	"context"
	"fmt"
)

// InboxCapacity is the depth of a cell's inbound signal queue. It bounds how
// many signals can wait for a cell that is not currently consuming them; a
// sender that reaches the limit is refused (synapse.ErrTargetBusy) rather than
// blocked, so a slow or idle cell applies backpressure instead of absorbing
// traffic in memory.
const InboxCapacity = 8

// ingest replaces this round's inbound track with whatever the cell's queue
// holds, taking the pairing route out of the loop's hands (a reply that answers
// a delegation is matched by the cell, not surfaced as a stimulus). It runs
// only at the Think gap, because that is the one place the queue's contents are
// guaranteed to be seen: the channel is the only store, so a signal arriving
// mid-round waits for the next Think rather than being taken out and left
// unread.
//
// Each stimulus taken is a task the cell is now working on (Working), and each
// one that names a requester is added to the invocation's answer debt — unlike
// Stimuli, that debt accumulates across rounds and survives a suspension.
func (lc *LoopContext) ingest() {
	lc.Stimuli = nil
	if lc.Ingest != nil {
		lc.Stimuli = lc.Ingest()
	}
	for i := range lc.Stimuli {
		s := lc.Stimuli[i]
		if s.Kind != KindStimulus {
			continue
		}
		s.Status = TaskWorking
		lc.Stimuli[i] = s
		if s.ID != "" && s.From != "" {
			lc.Requests = append(lc.Requests, s)
		}
	}
}

// answer reports this invocation's terminal to every peer request it served.
// One mapping decides the state (TaskOutcome), so the six task states have
// exactly one writer each and an organ never has to remember to report. An
// invocation that was abandoned mid-iteration has no lifecycle to report and
// sends nothing; an unroutable answer reaches the host through OnError rather
// than vanishing.
func (lc *LoopContext) answer(ctx context.Context, output string) {
	if len(lc.Requests) == 0 {
		return
	}
	status := TaskOutcome(lc.outcome, ctx.Err())
	if status == "" {
		return
	}
	if lc.Send == nil {
		lc.Hooks.OnError(ctx, fmt.Errorf("nerve: answer: %d request(s) served with no colony to reply through", len(lc.Requests)))
		return
	}
	// The caller's context may already be canceled (Close, a cancelled
	// Stimulate); the answer is the requester's only notice, so it goes out.
	ctx = context.WithoutCancel(ctx)
	for _, req := range lc.Requests {
		if _, err := lc.Send(ctx, Signal{To: req.From, ReplyTo: req.ID, Status: status, Payload: []byte(output)}); err != nil {
			lc.Hooks.OnError(ctx, fmt.Errorf("nerve: answer to %s: %w", req.ID, err))
		}
	}
}

// inhibitNames returns the tool names the notices on this round's track ask the
// loop to withhold.
func inhibitNames(signals []Signal) []string {
	var names []string
	for _, s := range signals {
		if s.Kind == KindNotice && len(s.Payload) > 0 {
			names = append(names, string(s.Payload))
		}
	}
	return names
}

// inhibited reports which notice on this round's track withholds the named
// tool. The check runs before a call is gated, so a veto never interrupts an
// in-flight Act — it refuses a call that has not started.
func (lc *LoopContext) inhibited(name string) (Signal, bool) {
	for _, s := range lc.Stimuli {
		if s.Kind == KindNotice && string(s.Payload) == name {
			return s, true
		}
	}
	return Signal{}, false
}

// inhibitedText is the canonical shape of a notice refusal: the brain reads who
// withheld the call (falling back to the notice's id when it names no sender).
func inhibitedText(s Signal) string {
	return "[inhibited: " + cmp.Or(s.From, s.ID) + "]"
}
