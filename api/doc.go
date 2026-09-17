// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// Package meowire is the public facade of the meowire harness: a decision-loop
// kernel (Think → Act event stream) plus the seven-port assembly surface. The
// framework owns the loop, the events and the wiring contract; the host owns
// every organ implementation and the multi-agent colony.
//
// Kernel: an internal/cell Cell holds an ID, the ports and a
// nerve.DecisionLoop, and yields iter.Seq[Event]. Every suspension — a tool
// waiting for input, a pause request, and the membrane's ask on either side of
// the loop — shares one primitive: the loop ends the iterator normally with a
// Session handle the host saves and returns to Resume.
//
// Assembly: New(Blueprint) is the sole composition root. Every port and every
// hook callback is required; a missing one is a missing organ, never a
// default. An assembly is checked against the wiring graph (ConnectomeNodes
// are the data objects, Connectome the slots) before an Agent exists.
//
// Dependency graph (strictly unidirectional):
//
//	meowire/api (facade + composition root)
//	  ├── internal/cell → internal/nerve
//	  └── internal/synapse → internal/nerve
package meowire
