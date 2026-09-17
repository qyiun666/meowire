// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// slots_sync_test.go — the runtime swap table and the wiring blueprint must
// describe the same slots. Replace is the only way to change an organ after
// assembly, so a slot missing from either side would silently widen or narrow
// what a host may swap without the assembly review ever reporting it.
package cell

import (
	"slices"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
)

func TestSlotTableMatchesBlueprint(t *testing.T) {
	var table []string
	for slot := range swapSlots {
		table = append(table, slot)
	}
	slices.Sort(table)

	want := nerve.SwappableSlots()
	slices.Sort(want)

	if !slices.Equal(table, want) {
		t.Fatalf("Replace accepts %v, blueprint advertises %v — the two must match", table, want)
	}
}

// TestUnknownSlotIsRefused proves the table lookup has teeth: a slot name the
// blueprint never advertised must error rather than no-op.
func TestUnknownSlotIsRefused(t *testing.T) {
	c := &Cell{}
	if _, err := c.Replace("closer", nil); err == nil {
		t.Fatal(`Replace("closer") must error — Closer is a resource binding, not a swappable slot`)
	}
}
