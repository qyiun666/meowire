// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// port.go — host ports: LLM/tool contracts.
package nerve

import "context"

// MethodSpec is a method specification (gene Method capability projection, describes only).
type MethodSpec struct {
	Name   string
	Desc   string
	Input  string
	Output string
}

// Prompt is the sole data package delivered to the brain (Thinker).
// The framework assembles it; the Thinker (LLM) makes decisions.
type Prompt struct {
	// Fixed part (set at construction, unchanged per cycle)
	System   string       // System instructions (host injected)
	Identity string       // Identity description text (host composed)
	Methods  []MethodSpec // Built-in capability description (gene projection, describes only)
	Tools    []ToolSpec   // Available tool list (host defined)
	Context  []string     // Context (host injected base; framework appends tool feedback within cycle)
	Bounds   string       // Execution boundary description (Sandbox.Bounds snapshot, host defined)

	// Dynamic part (updated each round)
	Input string // Current stimulus text
	State string // Current loop state (framework auto-updated)
	Plan  string // Task plan/progress (host injected, brain can update)
}

// ToolSpec is a tool specification (host defined, framework passthrough).
type ToolSpec struct {
	Name   string // Tool name
	Desc   string // Description
	Input  string // Input parameter description (JSON Schema)
	Output string // Output description
}

// Usage is the token usage (carried by Decision, host accumulates; nil = skip accounting).
type Usage struct {
	Prompt     int
	Completion int
	Total      int
}

// Decision is the Thinker output.
type Decision struct {
	Text      string
	ToolCalls []ToolCall
	Usage     *Usage
}

// ToolCall is a tool invocation declaration.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// Action is an execution action (wraps a tool call).
type Action struct {
	CellID string
	Call   ToolCall
}

// Effect is an execution result.
type Effect struct {
	Result string
	Err    string
}

// Thinker is the LLM host port (the brain).
type Thinker interface {
	Think(ctx context.Context, p *Prompt) (*Decision, error)
}

// Effector is the tool host port (execution).
type Effector interface {
	Act(ctx context.Context, a Action) (*Effect, error)
}

// Closer is the cleanup host port (shutdown).
type Closer interface {
	Close() error
}
