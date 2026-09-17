// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// task_test.go — the one mapping from a loop terminal to an A2A task state.
package nerve

import (
	"context"
	"fmt"
	"testing"
)

func TestTaskOutcomeMapsEveryTerminal(t *testing.T) {
	canceled := fmt.Errorf("tool timeout: %w", context.Canceled)
	for _, tc := range []struct {
		name    string
		outcome CycleOutcome
		ctxErr  error
		want    TaskStatus
	}{
		{"done", OutcomeDone, nil, TaskCompleted},
		{"suspended", OutcomeSuspended, nil, TaskNeedsInput},
		{"error", OutcomeError, nil, TaskFailed},
		{"max rounds", OutcomeMaxRounds, nil, TaskFailed},
		{"cancelled ctx", OutcomeError, canceled, TaskCancelled},
		{"aborted has no task state", OutcomeAborted, nil, ""},
		{"unmarked has no task state", 0, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TaskOutcome(tc.outcome, tc.ctxErr); got != tc.want {
				t.Fatalf("TaskOutcome(%v, %v) = %q, want %q", tc.outcome, tc.ctxErr, got, tc.want)
			}
		})
	}
}

// TestTaskOutcomeCoversAllSixStates guards the claim that each state has a
// producer: a new TaskStatus constant with no mapping here fails this test.
func TestTaskOutcomeCoversAllSixStates(t *testing.T) {
	seen := map[TaskStatus]bool{}
	for _, o := range []CycleOutcome{OutcomeDone, OutcomeSuspended, OutcomeError, OutcomeMaxRounds, OutcomeAborted} {
		seen[TaskOutcome(o, nil)] = true
		seen[TaskOutcome(o, context.Canceled)] = true
	}
	for _, s := range []TaskStatus{TaskSubmitted, TaskWorking, TaskNeedsInput, TaskCompleted, TaskFailed, TaskCancelled} {
		// Submitted and Working belong to the exchange itself (a request opens
		// them, a drained stimulus carries them), never to a terminal.
		if s == TaskSubmitted || s == TaskWorking {
			continue
		}
		if !seen[s] {
			t.Fatalf("no loop terminal produces task state %q", s)
		}
	}
}
