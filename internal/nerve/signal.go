// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// signal.go — neural signal: inter-individual message carrier.
//
// The framework does not consume Signal — inboxes and consumption are host
// responsibilities (flat model). Hosts use these types with synapse.Link/Fire
// or their own routing; sub-agent and multi-agent flows are host tools
// (spawn_agent / send_message) whose results return via EventToolResult.
package nerve

// Signal is the signal carrier in the bionic network.
type Signal struct {
	ID         string     // Unique identifier
	From       string     // Sender ID
	To         string     // Target ID
	Kind       SignalKind // Signal category
	Payload    []byte     // Content
	ErrPayload bool       // Structured error flag
}

// SignalKind categorizes signal types.
type SignalKind int

const (
	KindStimulus SignalKind = iota // Stimulus: host sends task to agent
	KindResponse                   // Response: agent reply
	KindNotice                     // Notice: side-channel message, no DecisionLoop
)

// Message and MessageRole are retained for host use. The framework does not consume them.

// MessageRole categorizes message roles.
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

// Message is a structured conversation message (retained for host use).
type Message struct {
	Role       MessageRole
	Content    string
	ToolCallID string
}
