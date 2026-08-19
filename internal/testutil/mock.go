// mock.go — shared test-only port stubs for meow package tests.
package testutil

import (
	"context"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Thinker is a test-only Thinker stub driven by a function field.
type Thinker struct {
	Fn func(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error)
}

// Think implements nerve.Thinker.
func (m Thinker) Think(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
	return m.Fn(ctx, p)
}

// Effector is a test-only Effector stub driven by a function field.
type Effector struct {
	Fn func(ctx context.Context, a nerve.Action) (*nerve.Effect, error)
}

// Act implements nerve.Effector.
func (m Effector) Act(ctx context.Context, a nerve.Action) (*nerve.Effect, error) {
	return m.Fn(ctx, a)
}

// Compile-time assertions: stubs implement the host port interfaces.
var _ nerve.Thinker = Thinker{}
var _ nerve.Effector = Effector{}
