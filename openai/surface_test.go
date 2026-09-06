// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// surface_test.go — pins the package's public surface: convergence
// discipline applies to the new face too. A signature change or removal
// must land here consciously.

package openai

import (
	meowire "github.com/qyiun666/meowire/api"
)

var (
	_ func(Config, ...Option) (*Thinker, error) = New
	_ func(func() bool) Option                  = WithStreamGate
	_ func(ChunkSink) Option                    = WithChunkSink
	_ func() Option                             = WithNoTools
	_ func(string) Wire                         = WireFromURL

	_ meowire.Thinker = (*Thinker)(nil)

	_ = Config{
		BaseURL: "", APIKey: "", Model: "",
		Wire:       WireChat,
		Timeout:    0,
		RetryMax:   0,
		BackoffMin: 0,
		BackoffMax: 0,
		Sampling:   Sampling{Temperature: 0, TopP: 0, MaxTokens: 0},
	}
	_ = Sampling{Temperature: 0, TopP: 0, MaxTokens: 0}
	_ = Chunk{Kind: ChunkText, Text: ""}
	_ = ChunkSink(nil)

	_ = WireChat
	_ = WireResponses
	_ = ChunkText
	_ = ChunkReasoning
)
