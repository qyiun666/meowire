// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// hook_test.go — the outcome vocabulary a host reads back.
package nerve

import (
	"slices"
	"testing"
)

// TestCycleOutcomeNamesCoverEveryOutcome: the name table is indexed by outcome
// value, so an outcome added to the iota without a name here would reach the host
// as "unknown" — this catches it at build time instead. The contents are pinned,
// not just the length: these words are what a host stores verbatim in its own
// memory organ, so renaming one is a decision about stored data.
func TestCycleOutcomeNamesCoverEveryOutcome(t *testing.T) {
	want := []string{"", "done", "suspended", "max_rounds", "error", "aborted"}
	if !slices.Equal(cycleOutcomeNames, want) {
		t.Fatalf("outcome names = %v, want %v", cycleOutcomeNames, want)
	}
	for o := OutcomeDone; o <= OutcomeAborted; o++ {
		if o.String() == "unknown" {
			t.Errorf("outcome %d has no name", int(o))
		}
	}
}

// TestCycleOutcomeZeroHasNoName: the zero value means no terminal point was
// reached, so it must not print as one of the five words.
func TestCycleOutcomeZeroHasNoName(t *testing.T) {
	if got := CycleOutcome(0).String(); got != "unknown" {
		t.Fatalf("zero value = %q, want unknown", got)
	}
	if got := CycleOutcome(len(cycleOutcomeNames)).String(); got != "unknown" {
		t.Fatalf("value past the table = %q, want unknown", got)
	}
}
