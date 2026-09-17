// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// budget.go — where the single regulator is applied.
package nerve

// metabolize applies the regulator to both accumulating tracks before a Think.
// The other per-round prompt inputs need no trimming by design: they are
// replaced wholesale each round rather than appended to (Reflection and Plan
// carry one current value, and any future cross-cell input track follows the
// same replace-per-round rule), so exactly two tracks grow within a cycle.
func (lc *LoopContext) metabolize() {
	lc.Context = lc.Budget.Trimmer(lc.Context, lc.Budget.MaxTokens)
	lc.ToolResults = lc.Budget.TrimResults(lc.ToolResults, lc.Budget.MaxTokens)
}
