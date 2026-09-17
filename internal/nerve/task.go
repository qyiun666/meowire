// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// task.go — the single mapping from a loop terminal to an A2A task state.
package nerve

import (
	"context"
	"errors"
)

// TaskOutcome is the framework's only writer of the closing task states: the
// way an invocation ended already determines how the task it was serving
// stands, so no organ has to report it.
//
// An abandoned iterator returns the empty status: the work stopped mid-flight
// without failing, and inventing a state for it would report a lifecycle that
// never happened. Such an invocation sends no answer at all.
func TaskOutcome(o CycleOutcome, ctxErr error) TaskStatus {
	switch o {
	case OutcomeDone:
		return TaskCompleted
	case OutcomeSuspended:
		return TaskNeedsInput
	case OutcomeError, OutcomeMaxRounds:
		if errors.Is(ctxErr, context.Canceled) {
			return TaskCancelled
		}
		return TaskFailed
	default:
		return ""
	}
}
