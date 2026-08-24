// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// sandbox.go — execution boundary: required port, the security membrane.
package nerve

import "context"

// Sandbox is the security membrane (required port): every tool execution
// passes through Allow before it runs. The host implements security
// policies; the framework provides the interception point and audits each
// decision via EventSandbox. Bounds() declares the execution boundary,
// surfaced to the Thinker via Prompt.Bounds once per Stimulate.
type Sandbox interface {
	Allow(ctx context.Context, a Action) (allowed bool, reason string, err error)
	// Bounds returns the execution boundary description (host defined),
	// surfaced to the Thinker via Prompt.Bounds once per Stimulate.
	Bounds() string
}
