// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// lifecycle.go — the two lifecycle timepoints the framework owns: bringing an
// organ up (Boot) and the order organs are asked to come up in.
package nerve

import "context"

// Bootable is an optional capability an organ may declare: it must open
// something before the agent can use it (a connection, a model handle, a warm
// cache). The framework calls Boot at the moment the instance joins an
// assembly — during New for the assembled organs, or immediately before a
// Replace commits the swap — and an instance joins exactly one assembly: a
// Bootable organ is built per agent, not shared across New calls. Reusing one
// instance would ask it to open its resources twice, and the framework cannot
// deduplicate what it does not own. A host that opens its resources earlier
// should simply not implement it.
//
// Boot is not the counterpart of Close. Cleanup has exactly one channel — the
// host's Closer port — because an organ is not a resource owner. When Boot
// fails mid-assembly, New releases what the attempt opened through that same
// Closer rather than learning to close organs one by one.
//
// Boot runs on a background context: how long a startup may take is the organ's
// own business (it owns the resource), not a deadline the framework guesses.
type Bootable interface {
	Boot(ctx context.Context) error
}

// PortOrder lists the required host ports in the sequence the framework brings
// them up: the blueprint's own phase-2 order (decide → act → resources → …),
// named by each port's blueprint Name. It is derived, never hand-written, so a
// port added to the blueprint joins the sequence here — and api's boot table
// is tested against this list, which is where a new port must be given an organ.
func PortOrder() []string {
	var order []string
	for _, wp := range Connectome() {
		if wp.Phase == 2 && wp.Required {
			order = append(order, wp.Name)
		}
	}
	return order
}
