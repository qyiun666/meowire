// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

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

// Sandbox is a test-only Sandbox stub: Fn drives Allow (nil = allow all);
// Bound is returned by Bounds ("" = no boundary declared).
type Sandbox struct {
	Fn    func(ctx context.Context, a nerve.Action) (bool, string, error)
	Bound string
}

// Allow implements nerve.Sandbox.
func (m Sandbox) Allow(ctx context.Context, a nerve.Action) (bool, string, error) {
	if m.Fn == nil {
		return true, "", nil
	}
	return m.Fn(ctx, a)
}

// Bounds implements nerve.Sandbox.
func (m Sandbox) Bounds() string { return m.Bound }

// Closer is a test-only Closer stub recording invocations.
type Closer struct {
	Called bool
}

// Close implements nerve.Closer.
func (c *Closer) Close() error {
	c.Called = true
	return nil
}

// Compile-time assertions: stubs implement the host port interfaces.
var _ nerve.Thinker = Thinker{}
var _ nerve.Effector = Effector{}
var _ nerve.Sandbox = Sandbox{}
var _ nerve.Closer = (*Closer)(nil)
