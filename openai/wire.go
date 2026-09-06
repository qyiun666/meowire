// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wire.go — LLM wire protocol enum, detection, and streamed chunk types.
package openai

import "strings"

// Wire is the LLM wire protocol: Chat Completions (widest compatibility
// surface) and the Responses API (native DeepSeek support). Detected from
// the base URL by WireFromURL, or pinned explicitly via Config.Wire.
type Wire string

const (
	WireChat      Wire = "chat"
	WireResponses Wire = "responses"
)

// WireFromURL detects the wire from the base URL: the official DeepSeek
// endpoint (api.deepseek.com) natively speaks /responses (semantic
// streaming events + a standard reasoning stream); every other vendor falls
// back to Chat Completions (Ollama/OpenAI/third-party gateways). Empty or
// unusual URLs fall back to WireChat. This is the single authority for
// protocol-shape detection — a new gateway supporting /responses adds a
// case here.
func WireFromURL(baseURL string) Wire {
	u := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(u, "api.deepseek.com") {
		return WireResponses
	}
	return WireChat
}

// ChunkKind classifies a streamed token delta: response body text and
// reasoning deltas are framed separately (the reasoning chain never mixes
// into the reply text).
type ChunkKind int

const (
	ChunkText      ChunkKind = iota // response body text delta
	ChunkReasoning                  // reasoning delta (deepseek-reasoner style)
)

// Chunk is one streamed token delta delivered to a ChunkSink. Chunks carry
// deltas only — the complete text always travels on the Decision, so the
// stream is an observability surface, never a data path.
type Chunk struct {
	Kind ChunkKind
	Text string
}
