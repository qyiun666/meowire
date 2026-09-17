// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// route.go — routing by declared capability: asking the colony "who can do X",
// and delivering to all of them.
package meowire

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// SkillIndex maps a capability name to the agents that declare it. It reads
// the same projection an Agent Card publishes (Methods → skills), so a peer a
// host can discover is a peer it can route to. The index is a snapshot of the
// agents passed in; adding a member to the colony means indexing again.
type SkillIndex struct {
	bySkill map[string][]string
}

// NewSkillIndex indexes a colony by capability. A nil member and a duplicate
// ID are refused — the same discipline as Resolve, because one name cannot
// address two cells. An agent declaring no capability simply appears under
// nothing.
func NewSkillIndex(agents ...*Agent) (*SkillIndex, error) {
	seen := make(map[string]struct{}, len(agents))
	bySkill := make(map[string][]string)
	for _, a := range agents {
		if a == nil {
			return nil, errors.New("meowire.NewSkillIndex: nil agent")
		}
		if _, dup := seen[a.ID()]; dup {
			return nil, fmt.Errorf("meowire.NewSkillIndex: duplicate agent id %q", a.ID())
		}
		seen[a.ID()] = struct{}{}
		for _, skill := range a.Skills() {
			bySkill[skill] = append(bySkill[skill], a.ID())
		}
	}
	for _, ids := range bySkill {
		slices.Sort(ids)
	}
	return &SkillIndex{bySkill: bySkill}, nil
}

// TargetsFor reports the agent IDs declaring skill, in ID order. An unknown
// capability has no targets — an empty list, not an error: this is a question
// about the colony, and "nobody" is the answer.
func (x *SkillIndex) TargetsFor(skill string) []string {
	return slices.Clone(x.bySkill[skill])
}

// FanOut delivers one signal to every target declaring sig.Skill, asking the
// synapse once per target. Each target is its own delivery, so a weak
// (ErrWeakSynapse), busy (ErrTargetBusy) or unlinked connection costs exactly
// that one target: the returned slice names who took the signal and the
// returned error joins one wrapped error per refusal.
//
// The sender is never a target of its own request. A capability nobody but the
// sender declares is an error, not an empty success — a fan-out that reached
// nobody did not happen.
//
// FanOut carries no answer back: the signal leaves without a minted id, so the
// target owes nothing. A cell that wants a reply delegates instead (see
// Effect.Send).
func (x *SkillIndex) FanOut(ctx context.Context, s Colony, sig Signal) ([]string, error) {
	switch {
	case sig.Skill == "":
		return nil, errors.New("meowire.FanOut: signal names no skill")
	case sig.To != "":
		return nil, fmt.Errorf("meowire.FanOut: skill routing takes no target (got %q)", sig.To)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("meowire.FanOut: %w", ctx.Err())
	}
	targets := withoutSelf(x.TargetsFor(sig.Skill), sig.From)
	if len(targets) == 0 {
		return nil, fmt.Errorf("meowire.FanOut: nobody but %q declares skill %q", sig.From, sig.Skill)
	}
	var delivered []string
	var errs []error
	for _, to := range targets {
		sig.To = to
		if err := s.Fire(ctx, sig); err != nil {
			errs = append(errs, fmt.Errorf("meowire.FanOut to %s: %w", to, err))
			continue
		}
		delivered = append(delivered, to)
	}
	return delivered, errors.Join(errs...)
}

// withoutSelf drops the sender from a target list (a cell asking for a
// capability it declares itself would answer its own request).
func withoutSelf(targets []string, from string) []string {
	return slices.DeleteFunc(targets, func(to string) bool { return to == from })
}
