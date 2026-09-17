// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// route_test.go — routing by declared capability: the index a colony asks
// "who can do X", and the fan-out that delivers to all of them.
package meowire

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// declared builds one agent whose only distinguishing feature is the skill
// names it declares.
func declared(t *testing.T, id string, skills ...string) *Agent {
	t.Helper()
	o := fullOrgans()
	o.ID = id
	for _, s := range skills {
		o.Methods = append(o.Methods, MethodSpec{Name: s})
	}
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("agent %s: %v", id, err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// recordingSynapse notes every delivery and refuses the targets it is told to.
type recordingSynapse struct {
	sent    []Signal
	refused map[string]error
}

func (r *recordingSynapse) Link(context.Context, string, string, float64) error { return nil }
func (r *recordingSynapse) Unlink(context.Context, string, string) error        { return nil }
func (r *recordingSynapse) Reinforce(context.Context, string, string, float64) error {
	return nil
}
func (r *recordingSynapse) Edges(context.Context, string) ([]Edge, error) { return nil, nil }
func (r *recordingSynapse) Fire(_ context.Context, sig Signal) error {
	if err, bad := r.refused[sig.To]; bad {
		return err
	}
	r.sent = append(r.sent, sig)
	return nil
}

func TestSkillIndexRoutesByCapability(t *testing.T) {
	handy := declared(t, "handy", "web_search", "summarize")
	scribe := declared(t, "scribe", "summarize")
	quiet := declared(t, "quiet")

	idx, err := NewSkillIndex(handy, scribe, quiet)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	// Both directions of the shared capability, and ID order, not insertion order.
	if got := idx.TargetsFor("summarize"); !slices.Equal(got, []string{"handy", "scribe"}) {
		t.Fatalf("summarize targets = %v, want [handy scribe]", got)
	}
	if got := idx.TargetsFor("web_search"); !slices.Equal(got, []string{"handy"}) {
		t.Fatalf("web_search targets = %v, want [handy]", got)
	}
	// A capability nobody declares is an empty answer, not an error, and the
	// snapshot is the caller's to keep.
	if got := idx.TargetsFor("never_declared"); len(got) != 0 {
		t.Fatalf("unknown capability targets = %v, want empty", got)
	}
	got := idx.TargetsFor("summarize")
	got[0] = "tampered"
	if second := idx.TargetsFor("summarize"); !slices.Equal(second, []string{"handy", "scribe"}) {
		t.Fatalf("index answered from a caller's slice: %v", second)
	}
	// An agent declaring nothing routes nowhere but stays a colony member.
	if skills := quiet.Skills(); len(skills) != 0 {
		t.Fatalf("quiet declares %v, want none", skills)
	}
}

// TestNewSkillIndexRefusesAmbiguousColony: the same discipline as Resolve —
// one name cannot address two cells, and nil is not a member.
func TestNewSkillIndexRefusesAmbiguousColony(t *testing.T) {
	a := declared(t, "twin", "x")
	twin := declared(t, "twin", "y")
	if _, err := NewSkillIndex(a, twin); err == nil {
		t.Fatal("index must refuse two agents sharing one id")
	}
	if _, err := NewSkillIndex(a, nil); err == nil {
		t.Fatal("index must refuse a nil member")
	}
}

func TestFanOutDeliversToEveryTarget(t *testing.T) {
	ctx := context.Background()
	asker := declared(t, "asker")
	handy := declared(t, "handy", "web_search")
	scribe := declared(t, "scribe", "web_search")
	idx, err := NewSkillIndex(asker, handy, scribe)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	syn := &recordingSynapse{refused: map[string]error{}}

	// One target is too weak to conduct: the other still gets the signal, and
	// the refusal is reported rather than absorbed.
	syn.refused["handy"] = ErrWeakSynapse
	delivered, err := idx.FanOut(ctx, syn, Signal{From: "asker", Skill: "web_search", Payload: []byte("look this up")})
	if !errors.Is(err, ErrWeakSynapse) {
		t.Fatalf("fan-out error = %v, want the weak-synapse refusal reported", err)
	}
	if !slices.Equal(delivered, []string{"scribe"}) {
		t.Fatalf("delivered = %v, want [scribe]", delivered)
	}
	if len(syn.sent) != 1 || syn.sent[0].To != "scribe" {
		t.Fatalf("sent = %+v, want one signal addressed to scribe", syn.sent)
	}
	if string(syn.sent[0].Payload) != "look this up" {
		t.Fatalf("payload = %q, want it carried to each target", syn.sent[0].Payload)
	}
}

// TestFanOutNeverAsksTheSender: a cell declaring the capability it asks for is
// not its own answer.
func TestFanOutNeverAsksTheSender(t *testing.T) {
	only := declared(t, "only", "web_search")
	idx, err := NewSkillIndex(only)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	syn := &recordingSynapse{}
	if _, err := idx.FanOut(context.Background(), syn, Signal{From: "only", Skill: "web_search"}); err == nil {
		t.Fatal("a fan-out that could reach nobody but the sender reported success")
	}
	if len(syn.sent) != 0 {
		t.Fatalf("sender was asked its own question: %+v", syn.sent)
	}
}

// TestFanOutRefusesAMisusedSignal: a named target is a delivery, not a
// fan-out, and a skillless signal has no route to fan out over.
func TestFanOutRefusesAMisusedSignal(t *testing.T) {
	handy := declared(t, "handy", "web_search")
	idx, err := NewSkillIndex(handy)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	syn := &recordingSynapse{}
	for _, sig := range []Signal{
		{From: "x", To: "handy"},                   // no skill named
		{From: "x", To: "handy", Skill: "other"},   // a target named anyway
		{From: "x", Skill: "nobody_declares_this"}, // nothing to reach
	} {
		if _, err := idx.FanOut(context.Background(), syn, sig); err == nil {
			t.Errorf("FanOut(%+v) reported success", sig)
		}
	}
	if len(syn.sent) != 0 {
		t.Fatalf("refused fan-outs still delivered: %+v", syn.sent)
	}
}

// TestAgentSkillsMatchesTheCard: the routing index and the published card read
// one projection, so a discoverable peer is a routable peer.
func TestAgentSkillsMatchesTheCard(t *testing.T) {
	o := fullOrgans()
	o.ID = "carded"
	o.Methods = []MethodSpec{{Name: "web_search", Desc: "look things up"}, {Name: "code_review"}}
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	defer a.Close()
	if got := a.Skills(); !slices.Equal(got, []string{"web_search", "code_review"}) {
		t.Fatalf("skills = %v, want the declared method names", got)
	}
	doc, err := AgentCard(o)
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	var card agentCard
	if err := json.Unmarshal(doc, &card); err != nil {
		t.Fatalf("unmarshal card: %v", err)
	}
	names := make([]string, 0, len(card.Skills))
	for _, s := range card.Skills {
		names = append(names, s.Name)
	}
	if !slices.Equal(names, a.Skills()) {
		t.Fatalf("card declares %v, the index routes %v", names, a.Skills())
	}
}
