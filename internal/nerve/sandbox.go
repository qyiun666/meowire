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

// Sandbox is the security membrane (required port): every tool execution
// passes through Allow before it runs. The host implements security
// policies; the framework provides the interception point and audits each
// decision via EventSandbox. A VerdictAsk suspends the loop at the gated
// call — the host resolves it through the same Resume channel as ask_user
// (empty response or a "[denied: ...]" payload resolves the ask as a
// denial; any other response approves execution). Bounds() declares the
// execution boundary, surfaced to the Thinker via Prompt.Bounds once per
// Stimulate.
type Sandbox interface {
	Allow(ctx context.Context, a Action) (verdict Verdict, reason string, err error)
	// Bounds returns the execution boundary description (host defined),
	// surfaced to the Thinker via Prompt.Bounds once per Stimulate.
	Bounds() string
}
