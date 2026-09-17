// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// signal.go — neural signal: inter-individual message carrier.
//
// A Signal is the envelope the synapse delivers into a cell's inbox; the
// framework pairs a response back to the request that awaits it (ReplyTo) and
// stamps the task lifecycle it owns (Status), while the routing table that
// chooses a target stays with the host.
package nerve

// Signal is the signal carrier in the bionic network.
// Status tracks the task lifecycle when the signal is a task exchange
// (A2A-style states; "" = not tracked).
type Signal struct {
	ID      string     // Unique identifier (minted by the emitting cell)
	From    string     // Sender ID
	To      string     // Target ID
	Kind    SignalKind // Signal category
	Status  TaskStatus // Task lifecycle state ("" = not tracked)
	ReplyTo string     // ID of the signal this one answers ("" = not a reply)
	Skill   string     // Capability requested of the target ("" = unqualified)
	Payload []byte     // Content
}

// TaskStatus tracks an inter-agent task lifecycle (A2A-style states,
// matching the Agent2Agent protocol task states).
type TaskStatus string

const (
	TaskSubmitted  TaskStatus = "submitted"   // Task created and queued
	TaskWorking    TaskStatus = "working"     // Task in progress
	TaskNeedsInput TaskStatus = "needs-input" // Awaiting input from the caller
	TaskCompleted  TaskStatus = "completed"   // Task finished successfully
	TaskFailed     TaskStatus = "failed"      // Task finished with an error
	TaskCancelled  TaskStatus = "cancelled"   // Task aborted before completion
)

// SignalKind categorizes signal types.
type SignalKind int

const (
	KindStimulus SignalKind = iota // Stimulus: host sends task to agent
	KindResponse                   // Response: agent reply
	KindNotice                     // Notice: side-channel message, no DecisionLoop
)
