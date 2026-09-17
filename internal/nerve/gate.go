// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// gate.go — the sandbox membrane: per-call ruling, audit record, denial feedback.
package nerve

import (
	"context"
	"fmt"
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

// deny records a final denial: the [sandbox-denied: ...] text joins the
// Context track (a verdict is not a tool result) and is yielded as structured
// feedback in place. Returns false when the consumer stopped.
func (b *actBatch) deny(tc ToolCall, fb string) bool {
	b.lc.Context = append(b.lc.Context, fb)
	return b.yield(Event{Kind: EventToolResult, Effect: &Effect{Err: fb}, ToolCall: &tc})
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
	if ruling == VerdictAsk {
		return VerdictAsk, "", reason, nil
	}
	return ruling, reason, "", nil
}
