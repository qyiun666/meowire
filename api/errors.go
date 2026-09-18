// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// errors.go — api package errors (re-export lower package errors).
package meowire

import (
	"github.com/qyiun666/meowire/internal/nerve"
)

// ErrCellClosed is what every refusal of a closed agent carries: Stimulate and
// Resume yield it as an EventError, Replace returns it. It is defined in the
// kernel and re-exported here for the same reason as the two below — internal/
// cannot be imported, so the errors.Is the documentation promises has to be
// spelled against a value the host can actually name.
var ErrCellClosed = nerve.ErrCellClosed

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = nerve.ErrMaxRounds

// ErrForeignSession is what Resume reports for a Session belonging to another
// cell. It is re-exported because the host's only way to tell "this handle is
// stale/misrouted" from "the organ failed" is to match the value — the loop
// wraps it into EventError, and the event wire restores it by identity.
var ErrForeignSession = nerve.ErrForeignSession
