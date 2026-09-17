// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// retry.go — Think/Act retry, per-attempt timeout, round cap and text truncation.
package nerve

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// effectiveMaxRounds returns DefaultMaxRounds if maxRounds <= 0.
func effectiveMaxRounds(maxRounds int) int {
	if maxRounds <= 0 {
		return DefaultMaxRounds
	}
	return maxRounds
}

// truncateText truncates a feedback payload to maxLen bytes, keeping UTF-8
// rune boundaries; appends a truncation marker. maxLen <= 0 = no truncation.
func truncateText(s string, maxLen int) string {
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	truncAt := maxLen
	for truncAt > 0 && !utf8.RuneStart(s[truncAt]) {
		truncAt--
	}
	if truncAt == 0 {
		// maxLen falls inside the first character; cut after the first rune.
		// Actual output may slightly exceed maxLen to keep UTF-8 intact.
		_, size := utf8.DecodeRuneInString(s)
		truncAt = size
	}
	return s[:truncAt] + fmt.Sprintf("[truncated, %d bytes total]", len(s))
}

// thinkWithRetry retries Think up to MaxRetries times; on ctx cancellation returns immediately.
func thinkWithRetry(ctx context.Context, lc *LoopContext, p *Prompt) (*Decision, error) {
	maxAttempts := lc.MaxRetries
	if maxAttempts < 0 {
		maxAttempts = 0 // negative config means "no retry", still execute once
	}
	var err error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("nerve.thinkWithRetry: %w", cerr)
		}
		var dec *Decision
		dec, err = lc.Think.Think(ctx, p)
		if err == nil {
			if dec == nil {
				err = fmt.Errorf("nerve: thinker returned nil decision without error")
				continue
			}
			return dec, nil
		}
	}
	return nil, fmt.Errorf("nerve.thinkWithRetry: %w", err)
}

// actWithRetry executes a tool with an optional per-attempt timeout and retry
// on effector errors only (Effect.Err is a business error and is never
// retried — retrying could duplicate side effects). A ctx cancellation or a
// timeout-derived error is not retried. The final error flows through
// ToolResults.Err like any other tool failure (resistance is feedback).
func actWithRetry(ctx context.Context, lc *LoopContext, act Action) (*Effect, error) {
	maxAttempts := lc.ToolMaxRetries
	if maxAttempts < 0 {
		maxAttempts = 0 // negative config means "no retry", still execute once
	}
	var eff *Effect
	var err error
	for attempt := 0; attempt <= maxAttempts; attempt++ {
		actCtx, cancel := ctx, func() {}
		if lc.ToolTimeout > 0 {
			actCtx, cancel = context.WithTimeout(ctx, lc.ToolTimeout)
		}
		eff, err = lc.Act.Act(actCtx, act)
		actErr := actCtx.Err() // capture BEFORE cancel: after cancel it is always non-nil
		cancel()               // released immediately; never deferred inside a retry loop
		if err == nil {
			if eff == nil {
				// Port-contract guard at the single Act chokepoint: success
				// without an Effect is surfaced as tool feedback, never as a
				// fabricated fallback result.
				eff = &Effect{Err: "nil effect from effector"}
			}
			return eff, nil
		}
		if ctx.Err() != nil || (lc.ToolTimeout > 0 && actErr != nil) {
			return eff, err // parent canceled or per-attempt timeout actually fired
		}
	}
	return eff, err
}
