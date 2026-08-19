// errors.go — api package errors (re-export lower package errors).
package meowire

import (
	"errors"

	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/synapse"
)

// ErrCellClosed is returned when Stimulate is called on a closed agent.
var ErrCellClosed = errors.New("meow: agent closed")

// ErrMaxRounds is returned when the loop exhausts all rounds with pending tool calls.
var ErrMaxRounds = nerve.ErrMaxRounds

// Synapse errors (defined in synapse, re-exported for facade users).
var (
	ErrNoTarget   = synapse.ErrNoTarget
	ErrNotLinked  = synapse.ErrNotLinked
	ErrTargetBusy = synapse.ErrTargetBusy
)
