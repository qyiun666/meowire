// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// correlation.go — the kernel half of an agent-to-agent exchange: a request
// this cell sent, the call that stopped to await its answer, and the answer
// once it lands.
package nerve

// Correlation is one delegation the framework is tracking, and — after the
// answer arrives — one resumption waiting for the host. The kernel pairs
// request to reply and carries the resume handle; deciding when (or whether)
// to resume stays with the host, so pairing is wiring and continuation is not.
type Correlation struct {
	// SignalID is the id this cell minted for the outbound request. It is the
	// key of the pairing and what Agent.Ack consumes.
	SignalID string
	// CellID is the delegating cell — the only one whose Resume accepts Session.
	CellID string
	// Call is the tool call that delegated; its result arrives as the answer.
	Call ToolCall
	// Session is the suspension handle of that call, captured at the moment
	// the request went out.
	Session Session
	// Status is the lifecycle state of the most recent reply that paired
	// (a task may report needs-input before it reports completed).
	Status TaskStatus
	// Response is that reply's payload. The kernel does not decode it: it is
	// whatever the answering side put there (its own answers carry text).
	Response []byte
}
