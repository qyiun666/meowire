// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// colony.go — colony wiring: the routing table a synapse delivers through.
package meowire

import (
	"errors"
	"fmt"
)

// Resolve builds the delivery table for a colony: every agent's ID mapped to
// its inbox. A synapse created over it — the table below fills in with
// SetResolver — lands a signal on the right neuron without the host writing the
// switch or owning the channels; the framework created both.
//
// The table needs live agents and an agent needs its Colony at assembly time,
// so a colony is wired in this order:
//
//	graph := meowire.NewDirect(meowire.DirectConfig{})
//	a, _ := meowire.New(blueprintWithColony(graph, "a"))
//	b, _ := meowire.New(blueprintWithColony(graph, "b"))
//	table, _ := meowire.Resolve(a, b)
//	graph.SetResolver(table)
//	graph.Link(ctx, "a", "b", 1)
//	graph.Link(ctx, "b", "a", 1)
//
// Link is one call per direction: a delegation travels the sender's edge, and
// the answer travels the responder's. Both edges are the host's topology
// decision, and a colony whose reply edge is missing strands the sender's
// suspended round (the responder's answer is what fails, and only there).
//
// Duplicate IDs are refused because they make delivery ambiguous: one name
// cannot address two cells. The table is a snapshot of the agents passed in;
// adding a member to the colony means resolving again.
func Resolve(agents ...*Agent) (Resolver, error) {
	byID := make(map[string]chan<- Signal, len(agents))
	for _, a := range agents {
		if a == nil {
			return nil, errors.New("meowire.Resolve: nil agent")
		}
		if _, dup := byID[a.ID()]; dup {
			return nil, fmt.Errorf("meowire.Resolve: duplicate agent id %q", a.ID())
		}
		byID[a.ID()] = a.cell.Inbox()
	}
	return func(id string) (chan<- Signal, bool) {
		ch, ok := byID[id]
		return ch, ok
	}, nil
}

// Resumptions reports the peer answers that arrived for requests this agent
// sent from inside a tool call: each carries the Session of the suspended call
// and the reply payload, so the host continues it with
// `Resume(ctx, r.Session, string(r.Response))` and then Ack(r.SignalID).
// Reading pairs what has arrived but consumes nothing — a task may report
// needs-input before it reports completed, and both pair against one request.
// The framework never resumes on its own: pairing is wiring, while when a
// suspended round may continue is the host's decision.
func (a *Agent) Resumptions() []Correlation {
	return a.cell.Resumptions()
}

// Ack ends one delegation: its answer stops being reported and a later reply
// for the same request is surfaced as an ordinary signal instead.
func (a *Agent) Ack(signalID string) {
	a.cell.Ack(signalID)
}
