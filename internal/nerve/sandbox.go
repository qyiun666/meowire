// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// sandbox.go — execution boundary declaration (optional port, nil = allow all).
package nerve

import "context"

// Sandbox is an execution boundary declaration (optional port, nil = allow all).
// The host implements security policies; the framework only provides the interception point.
type Sandbox interface {
	Allow(ctx context.Context, a Action) (allowed bool, reason string, err error)
	// Bounds returns the execution boundary description (host defined),
	// surfaced to the Thinker via Prompt.Bounds once per Stimulate.
	Bounds() string
}
