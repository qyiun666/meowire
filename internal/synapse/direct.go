// direct.go — Direct: a connection-table synapse with Resolver-based delivery.
package synapse

import (
	"context"
	"fmt"
	"sync"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Synapse errors.
var (
	ErrNoTarget   = fmt.Errorf("synapse: target not found")
	ErrNotLinked  = fmt.Errorf("synapse: not linked")
	ErrTargetBusy = fmt.Errorf("synapse: target inbox full")
)

// Resolver resolves a target ID to its signal inbox (host-injected closure,
// keeps synapse decoupled from cell).
type Resolver func(id string) (chan<- nerve.Signal, bool)

// Direct is a direct-connection synapse: a from→to link table plus a
// Resolver closure for delivery.
type Direct struct {
	mu      sync.RWMutex
	links   map[string]map[string]struct{}
	resolve Resolver
}

// NewDirect creates a Direct synapse. r may be nil until SetResolver is
// called at assembly time.
func NewDirect(r Resolver) *Direct {
	return &Direct{links: make(map[string]map[string]struct{}), resolve: r}
}

// SetResolver injects the Resolver (composition-root assembly).
func (d *Direct) SetResolver(r Resolver) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolve = r
}

// Link establishes a from→to connection (idempotent; rejects a cancelled ctx).
func (d *Direct) Link(ctx context.Context, from, to string) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Link: %w", ctx.Err())
	default:
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.links[from] == nil {
		d.links[from] = make(map[string]struct{})
	}
	d.links[from][to] = struct{}{}
	return nil
}

// Fire delivers a signal along an established connection. Delivery is
// non-blocking: it either succeeds immediately or fails immediately and
// never waits for the target to consume. If the target consumer is not
// running or the inbox is full, Fire returns ErrTargetBusy — hosts that
// need reliable delivery should retry or poll the target state.
func (d *Direct) Fire(ctx context.Context, sig nerve.Signal) error {
	d.mu.RLock()
	toSet, ok := d.links[sig.From]
	if !ok {
		d.mu.RUnlock()
		return fmt.Errorf("synapse.Direct.Fire: %w: %s -> %s", ErrNotLinked, sig.From, sig.To)
	}
	if _, ok := toSet[sig.To]; !ok {
		d.mu.RUnlock()
		return fmt.Errorf("synapse.Direct.Fire: %w: %s -> %s", ErrNotLinked, sig.From, sig.To)
	}
	resolve := d.resolve
	d.mu.RUnlock()
	if resolve == nil {
		return fmt.Errorf("synapse.Direct.Fire: nil resolver")
	}
	inbox, ok := resolve(sig.To)
	if !ok {
		return fmt.Errorf("synapse.Direct.Fire: %w: %s", ErrNoTarget, sig.To)
	}
	select {
	case inbox <- sig:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("synapse.Direct.Fire: %w", ctx.Err())
	default:
		return fmt.Errorf("synapse.Direct.Fire: %w: %s", ErrTargetBusy, sig.To)
	}
}

// connected reports whether a from→to connection exists.
func (d *Direct) connected(from, to string) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	toSet, ok := d.links[from]
	if !ok {
		return false
	}
	_, ok = toSet[to]
	return ok
}

// Connected reports whether a from→to connection exists (public read-only).
func (d *Direct) Connected(from, to string) bool { return d.connected(from, to) }

// Compile-time assertion: Direct implements Synapse.
var _ Synapse = (*Direct)(nil)
