// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// slots_sync_test.go — the blueprint's port slots, the Organs container and the
// Replace slot constants must agree. Ports are the only place where the
// framework says "the host provides this", so a required organ without an
// assembly field (or a slot name the blueprint does not carry) would let the
// public surface drift from the contract New validates against.
package meowire

import (
	"reflect"
	"slices"
	"testing"

	"github.com/qyiun666/meowire/internal/nerve"
)

// TestRequiredPortsHaveOrganFields checks every required phase-2 slot names an
// Organs field, so adding a port without a place to inject it fails here
// rather than in a host's build.
func TestRequiredPortsHaveOrganFields(t *testing.T) {
	organType := reflect.TypeOf(Organs{})
	fields := make(map[string]bool, organType.NumField())
	for i := 0; i < organType.NumField(); i++ {
		fields[organType.Field(i).Name] = true
	}
	for _, wp := range nerve.Connectome() {
		if wp.Phase != 2 || !wp.Required {
			continue
		}
		if !fields[wp.Name] {
			t.Errorf("required port %s (%s) has no Organs field of that name", wp.ID, wp.Name)
		}
		if _, wired := organFilled[wp.ID]; !wired {
			t.Errorf("required port %s has no filled-state predicate — Validate cannot see it", wp.ID)
		}
	}
}

// TestReplaceSlotConstantsMatchBlueprint pins the public slot strings to the
// blueprint's Slot field: the table in nerve/wire.go is the single source, and
// a constant that drifts from it would accept a swap the assembly review never
// advertised.
func TestReplaceSlotConstantsMatchBlueprint(t *testing.T) {
	got := []string{SlotThink, SlotAct, SlotSandbox, SlotBudget, SlotHooks}
	slices.Sort(got)

	want := nerve.SwappableSlots()
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Fatalf("api slot constants %v != blueprint slots %v", got, want)
	}
}

// TestImpliedSlotsAreMarkedNotRequired checks every slot the assembly skips is
// genuinely optional: an implied sub-slot that is Required would be checked
// twice and reported as a missing organ the host cannot supply separately.
func TestImpliedSlotsAreMarkedNotRequired(t *testing.T) {
	byID := make(map[string]nerve.WirePoint)
	for _, wp := range nerve.Connectome() {
		byID[wp.ID] = wp
	}
	for id := range impliedSlots {
		wp, ok := byID[id]
		if !ok {
			t.Fatalf("implied slot %s is not in the blueprint", id)
		}
		if wp.Required {
			t.Errorf("implied slot %s must not be Required (its parent port carries it)", id)
		}
	}
}
