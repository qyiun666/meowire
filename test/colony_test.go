// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// colony_test.go — the acceptance face of a colony: two agents assembled from
// organs alone, with a signal crossing between them. Everything the host of a
// multi-agent kernel would otherwise hand-wire (inboxes, the routing table, the
// pump that feeds a neighbour's traffic into a loop) is the framework's.
//
// The mechanical form of that criterion is TestColonyHostSurfaceIsWiringFree at
// the bottom of this file.
package meowire_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
	"github.com/qyiun666/meowire/internal/testutil"
)

// TestColonyDeliversWithoutHostPump assembles two agents, links them with a
// synapse over the framework-built routing table, and has A's tool fire a
// stimulus at B. B's Thinker must find that stimulus in its own prompt with no
// host code moving bytes between the two.
func TestColonyDeliversWithoutHostPump(t *testing.T) {
	ctx := context.Background()
	// A colony is circular: the routing table names the agents while each agent
	// carries the table. The reference graph breaks the circle at assembly —
	// created with no resolver, injected once the members exist.
	syn := meowire.NewDirect(meowire.DirectConfig{})

	agentA, err := testNew(colonyOrgans("a", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "delegated"}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "delegate"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(ctx context.Context, a meowire.Action) (*meowire.Effect, error) {
			if err := syn.Fire(ctx, meowire.Signal{
				From: "a", To: "b", Kind: meowire.KindStimulus, Payload: []byte("please help"),
			}); err != nil {
				return nil, err
			}
			return &meowire.Effect{Result: "delegated"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent a: %v", err)
	}
	defer agentA.Close()

	var saw meowire.Signal
	agentB, err := testNew(colonyOrgans("b", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.Stimuli) > 0 {
				saw = p.Stimuli[0]
			}
			return &meowire.Decision{Text: "helping"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent b: %v", err)
	}
	defer agentB.Close()

	resolver, err := meowire.Resolve(agentA, agentB)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	syn.SetResolver(resolver)
	if err := syn.Link(ctx, "a", "b", 1); err != nil {
		t.Fatalf("Link: %v", err)
	}

	for ev := range agentA.Stimulate(ctx, "solve it") {
		if ev.Kind == meowire.EventError {
			t.Fatalf("agent a errored: %v", ev.Err)
		}
	}
	// B goes about its own business. Nobody pumps B's inbox: the queue is the
	// framework's, and so is the drain at the Think gap.
	for ev := range agentB.Stimulate(ctx, "anything on your desk?") {
		if ev.Kind == meowire.EventError {
			t.Fatalf("agent b errored: %v", ev.Err)
		}
	}

	if string(saw.Payload) != "please help" || saw.From != "a" {
		t.Fatalf("B saw %+v, want A's stimulus delivered by the framework", saw)
	}
}

// colonyOrgans gives an agent the ID the colony routes by.
func colonyOrgans(id string, o meowire.Organs) meowire.Organs {
	merged := testOrgans(o)
	merged.ID = id
	return merged
}

// BEGIN wiring-free section: the host code between these markers may not name a
// signal field the framework owns, nor call Fire — TestColonyHostSurfaceIsWiringFree
// reads this file and enforces it.
//
// TestCrossCellChainWithoutHostCode walks a whole agent-to-agent task: A's tool
// delegates to B, A's round suspends, B's invocation answers on B's behalf, the
// framework pairs the answer back to A's suspended call, and only then does the
// host continue it. Nowhere does the host mint a signal id, choose a kind,
// report a task state or pump a queue.
func TestCrossCellChainWithoutHostCode(t *testing.T) {
	ctx := context.Background()
	syn := meowire.NewDirect(meowire.DirectConfig{})

	agentA, err := testNew(colonyOrgans("a", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "thanks: " + p.ToolResults[0].Result}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "delegate"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(_ context.Context, _ meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Send: &meowire.Signal{To: "b", Skill: "help", Payload: []byte("please help")}}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent a: %v", err)
	}
	defer agentA.Close()

	var received meowire.Signal
	var asked bool
	agentB, err := testNew(colonyOrgans("b", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.Stimuli) > 0 {
				received = p.Stimuli[0]
			}
			return &meowire.Decision{Text: "helping"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent b: %v", err)
	}
	defer agentB.Close()

	resolver, err := meowire.Resolve(agentA, agentB)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	syn.SetResolver(resolver)
	for _, edge := range [][2]string{{"a", "b"}, {"b", "a"}} {
		if err := syn.Link(ctx, edge[0], edge[1], 1); err != nil {
			t.Fatalf("Link %s→%s: %v", edge[0], edge[1], err)
		}
	}

	// A asks and stops: the round waits on the answer, the iterator ends.
	var sess meowire.Session
	for ev := range agentA.Stimulate(ctx, "solve it") {
		switch ev.Kind {
		case meowire.EventError:
			t.Fatalf("agent a errored: %v", ev.Err)
		case meowire.EventWaitInput:
			sess, asked = ev.Wait.Session, true
			if ev.Wait.Question != "awaiting reply from b" {
				t.Fatalf("question = %q, want the wait to name the peer", ev.Wait.Question)
			}
		}
	}
	if !asked {
		t.Fatal("no EventWaitInput: the delegation never suspended the round")
	}

	// B goes about its own business; the request arrives with a state B's host
	// never wrote, and its answer goes back out when B's invocation ends.
	for ev := range agentB.Stimulate(ctx, "anything on your desk?") {
		if ev.Kind == meowire.EventError {
			t.Fatalf("agent b errored: %v", ev.Err)
		}
	}
	if string(received.Payload) != "please help" || received.Skill != "help" {
		t.Fatalf("B received %+v, want A's request carried through", received)
	}
	if received.From != "a" || received.ID == "" {
		t.Fatalf("B received %+v, want the sending cell to have minted from and id", received)
	}
	if received.Status != meowire.TaskWorking {
		t.Fatalf("received status = %q, want the drained request to read as working", received.Status)
	}

	// The pairing waits for the host: same request id, B's output as the
	// answer, the suspended round attached.
	res := agentA.Resumptions()
	if len(res) != 1 {
		t.Fatalf("resumptions = %d, want the one answer B's invocation owed", len(res))
	}
	got := res[0]
	if got.SignalID != received.ID || got.Status != meowire.TaskCompleted || string(got.Response) != "helping" {
		t.Fatalf("resumption = %+v, want B's completion paired to A's request", got)
	}
	if got.Call.Name != "delegate" || got.CellID != "a" {
		t.Fatalf("resumption = %+v, want A's delegating call as the other half", got)
	}
	// The handle the pairing carries is the one the wait event yielded — the
	// answer comes back to the round that asked, not to a reconstruction.
	sessWire, err := sess.Marshal()
	if err != nil {
		t.Fatalf("suspend handle: %v", err)
	}
	pairedWire, err := got.Session.Marshal()
	if err != nil {
		t.Fatalf("paired handle: %v", err)
	}
	if !bytes.Equal(sessWire, pairedWire) {
		t.Fatal("the resumption carries another handle than the suspension yielded")
	}

	// Only now does the host continue the round, and Ack retires the pairing.
	var final strings.Builder
	var done bool
	for ev := range agentA.Resume(ctx, got.Session, string(got.Response)) {
		switch ev.Kind {
		case meowire.EventError:
			t.Fatalf("resume errored: %v", ev.Err)
		case meowire.EventText:
			final.WriteString(ev.Text)
		case meowire.EventDone:
			done = true
		}
	}
	if !done || final.String() != "thanks: helping" {
		t.Fatalf("resumed a=%v text=%q, want the answer digested into a Done round", done, final.String())
	}
	agentA.Ack(got.SignalID)
	if left := agentA.Resumptions(); len(left) != 0 {
		t.Fatalf("resumptions after Ack = %d, want 0", len(left))
	}
}

// END wiring-free section.

// TestDelegationWithoutColonyIsFeedback: asking with no delivery organ is
// resistance, not a hang — the call reports and the round keeps going.
func TestDelegationWithoutColonyIsFeedback(t *testing.T) {
	agent, err := testNew(colonyOrgans("loner", meowire.Organs{
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "improvised"}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "delegate"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(_ context.Context, _ meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Send: &meowire.Signal{To: "anyone"}}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	defer agent.Close()

	var failed string
	var done bool
	for ev := range agent.Stimulate(context.Background(), "solve it") {
		switch ev.Kind {
		case meowire.EventError:
			t.Fatalf("a refused delegation must not end the cycle: %v", ev.Err)
		case meowire.EventToolResult:
			if ev.Effect != nil {
				failed = ev.Effect.Err
			}
		case meowire.EventDone:
			done = true
		}
	}
	if !done {
		t.Fatal("no EventDone: the round did not carry on past the refusal")
	}
	if !strings.Contains(failed, "Colony") {
		t.Fatalf("tool feedback = %q, want the missing organ named", failed)
	}
}

// TestDelegationWithoutTargetIsFeedback: a send naming no target is refused
// before the colony is asked, and above all does not suspend. Asking "whoever
// can do X" is a host fan-out (SkillIndex.FanOut), not a delegation an answer
// can return to — a round that waited on it would wait forever.
func TestDelegationWithoutTargetIsFeedback(t *testing.T) {
	agent, err := testNew(colonyOrgans("vague", meowire.Organs{
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "improvised"}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "delegate"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(_ context.Context, _ meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Send: &meowire.Signal{Skill: "anything"}}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent: %v", err)
	}
	defer agent.Close()

	var failed string
	for ev := range agent.Stimulate(context.Background(), "solve it") {
		switch ev.Kind {
		case meowire.EventWaitInput:
			t.Fatal("an unaddressable delegation suspended the round")
		case meowire.EventError:
			t.Fatalf("a refused delegation must not end the cycle: %v", ev.Err)
		case meowire.EventToolResult:
			if ev.Effect != nil {
				failed = ev.Effect.Err
			}
		}
	}
	if !strings.Contains(failed, "To") {
		t.Fatalf("tool feedback = %q, want the missing target named", failed)
	}
}

// TestUnanswerableRequestIsReported: a cell that was asked something but has no
// way to answer says so out loud. The alternative — dropping the reply because
// the organ is missing — would leave the requester waiting on a question nobody
// admits to having received.
func TestUnanswerableRequestIsReported(t *testing.T) {
	ctx := context.Background()
	syn := meowire.NewDirect(meowire.DirectConfig{})

	asker, err := testNew(colonyOrgans("asker", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "asked"}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "ask"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(c context.Context, _ meowire.Action) (*meowire.Effect, error) {
			if err := syn.Fire(c, meowire.Signal{ID: "asker/1", From: "asker", To: "mute", Kind: meowire.KindStimulus, Payload: []byte("hello?")}); err != nil {
				return nil, err
			}
			return &meowire.Effect{Result: "sent"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("asker: %v", err)
	}
	defer asker.Close()

	var reported []error
	mute, err := testNew(colonyOrgans("mute", meowire.Organs{
		// No Colony organ: this cell can be asked things it cannot answer.
		Hooks: &meowire.Hooks{OnError: func(_ context.Context, e error) { reported = append(reported, e) }},
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.Stimuli) == 0 {
				t.Fatal("the request never reached the cell's prompt")
			}
			return &meowire.Decision{Text: "thinking out loud"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("mute: %v", err)
	}
	defer mute.Close()

	r, err := meowire.Resolve(asker, mute)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	syn.SetResolver(r)
	if err := syn.Link(ctx, "asker", "mute", 1); err != nil {
		t.Fatalf("Link: %v", err)
	}

	for ev := range asker.Stimulate(ctx, "delegate the greeting") {
		if ev.Kind == meowire.EventError {
			t.Fatalf("asker errored: %v", ev.Err)
		}
	}
	for ev := range mute.Stimulate(ctx, "your turn") {
		if ev.Kind == meowire.EventError {
			t.Fatalf("mute errored: %v", ev.Err)
		}
	}
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "reply") {
		t.Fatalf("reported = %v, want the unanswerable request named once", reported)
	}
}

// TestResolveRejectsAmbiguousColony: one ID cannot address two cells, and a nil
// member is not a member.
func TestResolveRejectsAmbiguousColony(t *testing.T) {
	a, err := testNew(colonyOrgans("twin", meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent a: %v", err)
	}
	defer a.Close()
	twin, err := testNew(colonyOrgans("twin", meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("agent twin: %v", err)
	}
	defer twin.Close()

	if _, err := meowire.Resolve(a, twin); err == nil {
		t.Fatal("Resolve must refuse two agents sharing one id")
	}
	if _, err := meowire.Resolve(a, nil); err == nil {
		t.Fatal("Resolve must refuse a nil member")
	}
	r, err := meowire.Resolve(a)
	if err != nil {
		t.Fatalf("Resolve(sole member): %v", err)
	}
	if _, ok := r("nobody"); ok {
		t.Fatal("resolver must not answer for an id outside the colony")
	}
}

// TestIdleAgentAppliesBackpressure: a cell that is not consuming keeps its queue
// bounded at InboxCapacity; the next delivery is refused rather than buffered in
// memory without limit. The backlog is not lost — the cell's next Think sees all
// of it.
func TestIdleAgentAppliesBackpressure(t *testing.T) {
	ctx := context.Background()
	var got int
	sender, err := testNew(colonyOrgans("sender", meowire.Organs{}), meowire.Config{})
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer sender.Close()
	listener, err := testNew(colonyOrgans("listener", meowire.Organs{
		Think: testutil.Thinker{Fn: func(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			got = len(p.Stimuli)
			return &meowire.Decision{Text: "done"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	defer listener.Close()

	r, err := meowire.Resolve(sender, listener)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	syn := meowire.NewDirect(meowire.DirectConfig{Resolver: r})
	if err := syn.Link(ctx, "sender", "listener", 1); err != nil {
		t.Fatalf("Link: %v", err)
	}
	sig := meowire.Signal{From: "sender", To: "listener", Kind: meowire.KindStimulus, Payload: []byte("ping")}
	for range meowire.InboxCapacity {
		if err := syn.Fire(ctx, sig); err != nil {
			t.Fatalf("Fire into a waiting queue: %v", err)
		}
	}
	if err := syn.Fire(ctx, sig); !errors.Is(err, meowire.ErrTargetBusy) {
		t.Fatalf("Fire past capacity = %v, want ErrTargetBusy", err)
	}
	for range listener.Stimulate(ctx, "what did you miss?") {
	}
	if got != meowire.InboxCapacity {
		t.Fatalf("stimuli drained = %d, want %d", got, meowire.InboxCapacity)
	}
}

// TestReplyEdgeMissingFailsWhereItIsObservable pins the one colony mistake a
// host can make silently in no other way: a delegation travels the sender's
// edge and its answer travels the responder's, so linking one direction only
// leaves the sender's round waiting. The framework does not own the graph and
// cannot invent the reply edge, but the refusal is never invisible — it is
// reported where the answer was attempted, and the sender is left with nothing
// to resume.
func TestReplyEdgeMissingFailsWhereItIsObservable(t *testing.T) {
	ctx := context.Background()
	syn := meowire.NewDirect(meowire.DirectConfig{})

	sender, err := testNew(colonyOrgans("a", meowire.Organs{
		Colony: syn,
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			if len(p.ToolResults) > 0 {
				return &meowire.Decision{Text: "done"}, nil
			}
			return &meowire.Decision{Text: "asking", ToolCalls: []meowire.ToolCall{{ID: "c1", Name: "delegate"}}}, nil
		}},
		Act: testutil.Effector{Fn: func(_ context.Context, _ meowire.Action) (*meowire.Effect, error) {
			return &meowire.Effect{Send: &meowire.Signal{To: "b", Skill: "help", Payload: []byte("please help")}}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("sender: %v", err)
	}
	defer sender.Close()

	var refused []error
	responder, err := testNew(colonyOrgans("b", meowire.Organs{
		Colony: syn,
		Hooks:  &meowire.Hooks{OnError: func(_ context.Context, err error) { refused = append(refused, err) }},
		Think: testutil.Thinker{Fn: func(_ context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
			return &meowire.Decision{Text: "here you are"}, nil
		}},
	}), meowire.Config{})
	if err != nil {
		t.Fatalf("responder: %v", err)
	}
	defer responder.Close()

	resolver, err := meowire.Resolve(sender, responder)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	syn.SetResolver(resolver)
	if err := syn.Link(ctx, "a", "b", 1); err != nil {
		t.Fatalf("Link a→b: %v", err)
	}

	waited := false
	for ev := range sender.Stimulate(ctx, "solve it") {
		if ev.Kind == meowire.EventWaitInput {
			waited = true
		}
	}
	if !waited {
		t.Fatal("the delegation did not suspend the sender's round")
	}
	for range responder.Stimulate(ctx, "your turn") {
	}

	if len(refused) == 0 {
		t.Fatal("an undeliverable answer vanished without a trace")
	}
	if !errors.Is(refused[0], meowire.ErrNotLinked) {
		t.Fatalf("answer failure = %v, want the missing reply edge named", refused[0])
	}
	if got := sender.Resumptions(); len(got) != 0 {
		t.Fatalf("sender has %d resumptions, want none: nothing answered it", len(got))
	}
}

// TestColonyHostSurfaceIsWiringFree is the mechanical reading of the acceptance
// criterion: hosting this colony never means declaring a channel, spawning a
// goroutine, or receiving from a queue.
func TestColonyHostSurfaceIsWiringFree(t *testing.T) {
	src, err := os.ReadFile("colony_test.go")
	if err != nil {
		t.Fatalf("read self: %v", err)
	}
	text := string(src)
	// The forbidden tokens are built at runtime so this file does not contain
	// what it forbids.
	for _, forbidden := range []string{"chan " + "meowire.Signal", "go " + "func", "<" + "-"} {
		if i := strings.Index(text, forbidden); i >= 0 {
			t.Fatalf("host-side wiring found (%q at offset %d): a colony must assemble from organs alone", forbidden, i)
		}
	}
	// Inside the marked section the host does not even name a signal field the
	// framework owns: it states a target and a payload, nothing else.
	start := strings.Index(text, "BEGIN wiring-free section")
	end := strings.Index(text, "END wiring-free section")
	if start < 0 || end < 0 || end < start {
		t.Fatal("wiring-free markers missing or misplaced: the guard would silently stop checking")
	}
	section := text[start:end]
	for _, forbidden := range []string{"Fire" + "(", "Kin" + "d:", "Stat" + "us:", "Fr" + "om:"} {
		if i := strings.Index(section, forbidden); i >= 0 {
			t.Fatalf("host code in the wiring-free section names %q at offset %d: that field belongs to the framework", forbidden, i)
		}
	}
}
