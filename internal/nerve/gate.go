// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// gate.go — the sandbox membrane: per-call ruling, audit record, denial feedback.
package nerve

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// gateResult is one call's membrane outcome. The audit record was already
// yielded by the membrane, so the caller only acts on the ruling.
type gateResult struct {
	act      Action  // pre-built action (BeforeAct still pending)
	ruling   Verdict // Deny / Allow / Ask
	reason   string  // deny policy reason
	question string  // ask confirmation prompt (Ruling == Ask)
	ok       bool    // false = consumer stopped (yield refused); end everything
}

// gate consults the membrane for one call and yields exactly one EventSandbox
// audit record for every ruling (Allow, Deny, Ask; a sandbox error coerces to
// a fail-closed Deny). Callers own what happens next — denial feedback, ask
// suspension, or BeforeAct plus execution — because the suspension tail
// differs per path.
func (b *actBatch) gate(tc ToolCall) gateResult {
	lc := b.lc
	g := gateResult{act: Action{CellID: lc.CellID, Call: tc}, ok: true}
	ruling, reason, question, sbErr := consultSandbox(b.ctx, lc, tc)
	v := &SandboxVerdict{CellID: lc.CellID, Call: tc, Ruling: ruling, Reason: reason, Question: question, Err: sbErr}
	if !b.yield(Event{Kind: EventSandbox, Verdict: v}) {
		g.ok = false
		return g
	}
	g.ruling, g.reason, g.question = ruling, reason, question
	return g
}

// refuse records a final refusal: the text joins the Context track (a refusal
// is not a tool result) and is yielded as structured feedback in place. Returns
// false when the consumer stopped.
func (b *actBatch) refuse(tc ToolCall, fb string) bool {
	b.lc.Context = append(b.lc.Context, fb)
	return b.yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}, ToolCall: &tc})
}

// deniedText is the canonical shape of a membrane denial, on both sides of the
// loop: the brain reads the same prefix for a withheld call and a withheld
// sentence.
func deniedText(reason string) string { return "[sandbox-denied: " + reason + "]" }

// emitGate rules on this round's utterance before anyone hears it, yielding
// exactly one EventSandbox audit record per ruling. It returns the ruling and
// the text that takes its place downstream: the draft on Allow, the denial on
// Deny, the question on Ask. ok=false means the consumer stopped.
//
// The audit record never carries the withheld draft — the draft may be exactly
// what the membrane exists to stop.
func (b *actBatch) emitGate(text string) (ruling Verdict, say string, ok bool) {
	lc := b.lc
	ruling, reason, question, sbErr := consultEmit(b.ctx, lc, Utterance{CellID: lc.CellID, Round: b.round, Text: text})
	v := &SandboxVerdict{CellID: lc.CellID, Ruling: ruling, Reason: reason, Question: question, Err: sbErr}
	if !b.yield(Event{Kind: EventSandbox, Verdict: v}) {
		return ruling, "", false
	}
	switch ruling {
	case VerdictAllow:
		return ruling, text, true
	case VerdictAsk:
		return ruling, question, true
	}
	return ruling, deniedText(reason), true
}

// utter rules one round's text through the output membrane and says the result:
// the draft on Allow, the denial text on Deny (which also joins the context
// track, so the brain reads the veto next Think), or a suspension with the
// draft withheld on Ask. It returns false when the loop must end.
func (b *actBatch) utter(dec *Decision) bool {
	ruling, say, ok := b.emitGate(dec.Text)
	if !ok {
		return false
	}
	switch ruling {
	case VerdictAsk:
		// The round's tool calls were never gated; they wait behind the draft.
		_, _ = b.suspend(suspension{remaining: dec.ToolCalls, question: say, kind: waitUtterance, utterance: dec.Text})
		return false
	case VerdictDeny:
		b.lc.Context = append(b.lc.Context, say)
	}
	b.out.WriteString(say)
	return b.yield(Event{Kind: EventText, Text: say})
}

// consultSandbox evaluates the membrane policy for one call and maps it onto
// the tri-state ruling: a sandbox evaluation error coerces to a fail-closed
// Deny (reason carries the error text, sbErr preserves the raw failure for
// the audit record). The question is non-empty only for an Ask ruling — the
// host's reason doubles as the confirmation prompt then.
func consultSandbox(ctx context.Context, lc *LoopContext, tc ToolCall) (ruling Verdict, reason string, question string, sbErr error) {
	act := Action{CellID: lc.CellID, Call: tc}
	ruling, reason, err := lc.Sandbox.Allow(ctx, act)
	if err != nil {
		return VerdictDeny, fmt.Sprintf("sandbox error: %v", err), "", err
	}
	if ruling, reason = knownRuling(ruling, reason); ruling == VerdictAsk {
		return VerdictAsk, "", reason, nil
	}
	return ruling, reason, "", nil
}

// knownRuling keeps the port contract's promise in one place: a membrane answer
// this build has no name for has not permitted anything, so it rules as Deny
// rather than falling through a switch's default branch into execution.
func knownRuling(ruling Verdict, reason string) (Verdict, string) {
	if verdictName(ruling) == "" {
		return VerdictDeny, fmt.Sprintf("sandbox returned an unknown ruling %d", int(ruling))
	}
	return ruling, reason
}

// consultEmit is consultSandbox on the output side: an Emit error coerces to a
// fail-closed Deny, and an Ask ruling carries its reason as the question text.
func consultEmit(ctx context.Context, lc *LoopContext, u Utterance) (ruling Verdict, reason string, question string, sbErr error) {
	ruling, reason, err := lc.Sandbox.Emit(ctx, u)
	if err != nil {
		return VerdictDeny, fmt.Sprintf("sandbox error: %v", err), "", err
	}
	if ruling, reason = knownRuling(ruling, reason); ruling == VerdictAsk {
		return VerdictAsk, "", reason, nil
	}
	return ruling, reason, "", nil
}

// resolveAsk is the confirmation response grammar shared by both ask chains: an
// empty response denies as "declined", a "[denied: ...]" payload denies with
// that text, anything else approves (the second return value is the denial text
// to land, or the response itself when approved).
func resolveAsk(response string) (Verdict, string) {
	resp := strings.TrimSpace(response)
	switch {
	case resp == "":
		return VerdictDeny, deniedText("declined")
	case strings.HasPrefix(resp, "[denied:"):
		return VerdictDeny, "[sandbox-denied" + strings.TrimPrefix(resp, "[denied")
	default:
		return VerdictAllow, resp
	}
}

// resolveCallAsk closes a pre-execution confirmation chain: the approved call
// runs through the admitted pipeline without re-gating (a stateless membrane
// would ask forever), a denial lands its feedback in place. Either arm yields
// the terminal EventSandbox closing the chain. It returns false when the caller
// must stop — the consumer left, or the arm suspended again.
func (b *actBatch) resolveCallAsk(sess Session, response string) bool {
	ruling, text := resolveAsk(response)
	if !b.yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
		CellID: b.lc.CellID, Call: sess.pending, Ruling: ruling, Reason: text,
	}}) {
		return false
	}
	if ruling != VerdictAllow {
		return b.refuse(sess.pending, text)
	}
	b.lc.State = StateActing
	if !b.yield(Event{Kind: EventState, State: StateActing}) {
		return false
	}
	waiting, ok := b.admitted(sess.pending, slices.Clone(sess.remaining))
	return ok && waiting == nil
}

// resolveUtteranceAsk closes an output confirmation chain: approval says the
// withheld draft as generated, a denial replaces it with the veto text (which
// also joins the context track, so the brain reads it next Think). It reports
// done when the round had nothing left to execute — the cycle ends there rather
// than entering another Think — and ok=false when the consumer stopped.
func (b *actBatch) resolveUtteranceAsk(sess Session, response string) (done, ok bool) {
	ruling, text := resolveAsk(response)
	if !b.yield(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{
		CellID: b.lc.CellID, Ruling: ruling, Reason: text,
	}}) {
		return false, false
	}
	said := sess.utterance
	if ruling != VerdictAllow {
		b.lc.Context = append(b.lc.Context, text)
		said = text
	}
	b.out.WriteString(said)
	if !b.yield(Event{Kind: EventText, Text: said}) {
		return false, false
	}
	return len(sess.remaining) == 0, true
}
