// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// compose.go — organ combinators: many implementations behind the one port
// the loop sees. Composition, not routing: the framework never learns which
// organ answered, only that a stack of membranes ruled as one and that a tool
// call reached an organ that could carry it out.
//
// Every combinator returns an existing port type, so a composed organ is
// interchangeable with a plain one — `Replace("act", FallbackEffector(a, b))`
// works because the slot table never knew the difference. The brain has no
// combinator: it is the one organ the framework itself ships, and there is
// nothing behind it to fall back to.
package nerve

import (
	"context"
	"errors"
	"fmt"
)

// GuardStack composes membranes into one port. Rulings are ordered by
// severity, not by layer: Deny beats Ask beats Allow, and the first Deny
// short-circuits — a later, more permissive layer never gets a vote. A layer
// returning an error is ruled a fail-closed Deny by the port contract itself,
// which ends the stack for the same reason. Bounds() reports the first
// non-empty boundary: one description per agent, the strictest author wins.
//
// An empty stack denies (a membrane with no layers guards nothing).
//
// A combinator never forwards Boot: the framework asserts the lifecycle on the
// port value it is handed, so a member that must come up first is booted before
// it is composed.
func GuardStack(layers ...Sandbox) Sandbox { return guardStack(layers) }

type guardStack []Sandbox

func (g guardStack) Allow(ctx context.Context, a Action) (Verdict, string, error) {
	return g.rule(func(l Sandbox) (Verdict, string, error) { return l.Allow(ctx, a) })
}

func (g guardStack) Emit(ctx context.Context, u Utterance) (Verdict, string, error) {
	return g.rule(func(l Sandbox) (Verdict, string, error) { return l.Emit(ctx, u) })
}

// rule applies the severity order to one side of the loop.
func (g guardStack) rule(ask func(Sandbox) (Verdict, string, error)) (Verdict, string, error) {
	if len(g) == 0 {
		return VerdictDeny, "no membrane layer wired", nil
	}
	var asked string
	var askSeen bool
	for _, layer := range g {
		verdict, reason, err := ask(layer)
		if err != nil {
			return VerdictDeny, sandboxErrText(err), nil
		}
		verdict, reason = knownRuling(verdict, reason)
		switch verdict {
		case VerdictDeny:
			return VerdictDeny, reason, nil
		case VerdictAsk:
			if !askSeen {
				asked, askSeen = reason, true
			}
		}
	}
	if askSeen {
		return VerdictAsk, asked, nil
	}
	return VerdictAllow, "", nil
}

func (g guardStack) Bounds() string {
	for _, layer := range g {
		if b := layer.Bounds(); b != "" {
			return b
		}
	}
	return ""
}

// FallbackEffector tries each tool port in order until one executes without an
// execution error. A business failure — `Effect.Err`, the tool having run and
// said no — is a result, not a broken organ, so it never moves to the next
// member (mirroring the loop's own no-retry-on-Effect.Err discipline).
func FallbackEffector(ports ...Effector) Effector { return fallbackEffector(ports) }

type fallbackEffector []Effector

func (f fallbackEffector) Act(ctx context.Context, a Action) (*Effect, error) {
	if len(f) == 0 {
		return nil, errors.New("nerve: fallback effector has no member wired")
	}
	var errs []error
	for i, port := range f {
		eff, err := port.Act(ctx, a)
		if err == nil {
			return eff, nil
		}
		errs = append(errs, fmt.Errorf("member %d: %w", i, err))
	}
	return nil, errors.Join(errs...)
}
