// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// errors.go — api package errors (re-export lower package errors).
package meowire

import (
	"errors"

	"github.com/qyiun666/meowire/internal/nerve"
)

// ErrCellClosed is returned when Stimulate is called on a closed agent.
var ErrCellClosed = errors.New("meow: agent closed")

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = nerve.ErrMaxRounds

// ErrForeignSession is what Resume reports for a Session belonging to another
// cell. It is re-exported because the host's only way to tell "this handle is
// stale/misrouted" from "the organ failed" is to match the value — the loop
// wraps it into EventError, and the event wire restores it by identity.
var ErrForeignSession = nerve.ErrForeignSession
