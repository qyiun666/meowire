// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// context.go — context size limit (optional port, nil = no trimming).
package nerve

// ContextBudget is a context size limit (optional port, nil = no trimming).
// The host provides the trimming strategy; the framework calls it before each Think.
type ContextBudget struct {
	MaxTokens int
	Trimmer   func([]string, int) []string
}
