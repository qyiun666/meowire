// sandbox.go — execution boundary declaration (optional port, nil = allow all).
package nerve

import "context"

// Sandbox is an execution boundary declaration (optional port, nil = allow all).
// The host implements security policies; the framework only provides the interception point.
type Sandbox interface {
	Allow(ctx context.Context, a Action) (allowed bool, reason string, err error)
}
