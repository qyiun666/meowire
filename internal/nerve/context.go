// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// context.go — context size budget: required port, the metabolic regulator.
package nerve

// ContextBudget is the context size budget (required port): the framework
// calls Trimmer before every Think, keeping the context within MaxTokens.
// A budget with a nil Trimmer or MaxTokens <= 0 fails assembly (no
// trimming = no budget).
type ContextBudget struct {
	MaxTokens int
	Trimmer   func([]string, int) []string
}
