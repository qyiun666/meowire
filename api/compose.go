// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// compose.go — organ combinators at the facade: several implementations behind
// the one port the loop sees, plus the lifecycle capability an organ may
// declare. These compose existing ports, so a composed organ is swappable in
// the same slot as a plain one.
package meowire

import "github.com/qyiun666/meowire/internal/nerve"

// Organ combinators (composition, not routing — the framework never learns
// which member answered).
var (
	// GuardStack rules as one membrane: Deny beats Ask beats Allow, and the
	// first Deny short-circuits the layers behind it.
	GuardStack = nerve.GuardStack
	// FallbackThinker tries brains in order until one answers; an execution
	// error moves on, all failures are reported together.
	FallbackThinker = nerve.FallbackThinker
	// FallbackEffector tries tool ports in order until one executes; a tool
	// that ran and refused (Effect.Err) is a result and never retried elsewhere.
	FallbackEffector = nerve.FallbackEffector
)

// Bootable is the optional lifecycle capability: New calls Boot on each organ
// that implements it, in PortOrder, before the agent exists; Replace calls it
// before a swap commits. Cleanup stays the Closer port's alone — see the
// internal contract for why Boot is not Close's counterpart.
type Bootable = nerve.Bootable

// PortOrder reports the sequence the framework brings the required host ports
// up in (blueprint order: Think, Act, Closer, Hooks, Sandbox, Budget, Mem).
func PortOrder() []string { return nerve.PortOrder() }
