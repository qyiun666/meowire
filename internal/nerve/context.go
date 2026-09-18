// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// context.go — context size budget: required port, the metabolic regulator.
package nerve

// ContextBudget is the context size budget (required port): one regulator over
// the two tracks that grow within a cycle. Trimmer shrinks the text track (host
// base + sandbox denials); TrimResults shrinks the structured tool-feedback
// track. Both run at the same checkpoint — before every Think — against the same
// MaxTokens, because a budget that regulates only half of what reaches the
// brain is not a budget. The arithmetic is the organ's own: the loop never
// re-reads a trimmed-away entry, and a suspension snapshot holds whatever the
// last trim left.
//
// A nil Trimmer or TrimResults, or MaxTokens <= 0, fails assembly.
type ContextBudget struct {
	MaxTokens   int
	Trimmer     func([]string, int) []string
	TrimResults func([]ToolResult, int) []ToolResult
}

// CompleteBudget reports whether the regulator can regulate: a trimmer per
// growing track and a positive limit. The api's Validate and the cell's
// Replace assertion both ask here, so the assembly-time rule and the
// runtime-swap rule are one rule.
func CompleteBudget(b *ContextBudget) bool {
	return b != nil && b.Trimmer != nil && b.TrimResults != nil && b.MaxTokens > 0
}
