// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// synapse.go — inter-agent connection and signal delivery contract.
package synapse

import (
	"context"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Synapse is the inter-agent messaging capability. Link establishes a
// from→to connection; Fire delivers a signal along an established
// connection. Implementations are wired by the composition root.
type Synapse interface {
	Link(ctx context.Context, from, to string) error
	Fire(ctx context.Context, sig nerve.Signal) error
}
