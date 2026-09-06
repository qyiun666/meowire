// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// Package openai ships the reference Thinker for the meowire decision loop:
// a zero-dependency OpenAI-compatible LLM client that turns a Prompt into a
// Decision over both OpenAI-family wires (Chat Completions and the
// Responses API). Hosts assemble it and fill the Prompt; the Thinker port
// stays open for hosts that need custom prompting or transports.
//
// Streaming: with a stream gate installed, every Think re-evaluates it —
// token deltas flow to the sink (text and reasoning framed separately)
// while the complete text always travels on the Decision. A blocking sink
// applies backpressure to the loop; ctx cancellation is its short-circuit.
package openai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	meowire "github.com/qyiun666/meowire/api"
)

// Config is the Thinker construction configuration (assembly root injects).
type Config struct {
	BaseURL string // e.g. https://api.openai.com/v1 — trailing slash tolerated
	APIKey  string // bearer token; empty = no Authorization header
	Model   string

	// Wire pins the protocol; empty = WireFromURL(BaseURL).
	Wire Wire

	// Transport tuning (zero values resolve to the defaults documented per field).
	Timeout    time.Duration // per attempt: whole request when non-streaming, time-to-headers when streaming (0 = 120s)
	RetryMax   int           // transient-failure retries (0 = 3, negative = 0)
	BackoffMin time.Duration // 0 = 1s
	BackoffMax time.Duration // 0 = 8s

	// Sampling rides every request; zero fields are not sent (these
	// protocol fields cannot express 0 vs unset) — the vendor default wins.
	Sampling Sampling
}

// Sampling is the LLM request sampling parameters.
type Sampling struct {
	Temperature float32
	TopP        float32
	MaxTokens   int
}

// ChunkSink receives streamed token deltas. Implementations should select
// on ctx: a blocking sink applies backpressure to the decision loop and ctx
// cancellation is the only short-circuit.
type ChunkSink func(ctx context.Context, c Chunk)

// Option customizes the Thinker.
type Option func(*Thinker)

// WithStreamGate installs the per-Think streaming decision: gate() true →
// stream, false → single-shot. Evaluated on every Think (the consumer's
// subscription may change between rounds). No gate = always single-shot.
func WithStreamGate(gate func() bool) Option {
	return func(t *Thinker) { t.gate = gate }
}

// WithChunkSink installs the token-delta receiver (ChunkSink contract).
func WithChunkSink(sink ChunkSink) Option {
	return func(t *Thinker) { t.sink = sink }
}

// WithNoTools limits the Thinker to plain conversation: tool definitions
// are stripped from every Prompt, so the server can never return
// structured tool calls (lightweight-model flavor).
func WithNoTools() Option {
	return func(t *Thinker) { t.noTools = true }
}

// Thinker is the reference implementation of the meowire Thinker port
// (the brain). Concurrency-safe: the transport is an http.Client, every
// other field is read-only after New.
type Thinker struct {
	http    *retryClient
	cfg     Config
	gate    func() bool
	sink    ChunkSink
	noTools bool
}

// New constructs a Thinker; BaseURL and Model are required.
func New(cfg Config, opts ...Option) (*Thinker, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("openai: BaseURL is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openai: Model is required")
	}
	t := &Thinker{http: newRetryClient(cfg), cfg: cfg}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// Think implements meowire.Thinker: render the Prompt into a wire request,
// call the LLM, parse the Decision. The wire (chat/responses) and the
// streaming path (once/stream) are orthogonal dispatches.
func (t *Thinker) Think(ctx context.Context, p *meowire.Prompt) (*meowire.Decision, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("openai: %w", err)
	}
	if t.noTools { // plain-conversation mode: strip tool definitions without
		// touching the framework's Prompt (local copy)
		cp := *p
		cp.Tools = nil
		p = &cp
	}
	if t.wire() == WireResponses {
		req := renderResponses(t.cfg, p)
		if t.streaming() {
			return t.respStream(ctx, req)
		}
		return t.respOnce(ctx, req)
	}
	req := renderChat(t.cfg, p)
	if t.streaming() {
		return t.chatStream(ctx, req)
	}
	return t.chatOnce(ctx, req)
}

func (t *Thinker) wire() Wire {
	if t.cfg.Wire != "" {
		return t.cfg.Wire
	}
	return WireFromURL(t.cfg.BaseURL)
}

// streaming re-evaluates the gate on every Think.
func (t *Thinker) streaming() bool {
	return t.gate != nil && t.gate()
}

// emit delivers one token delta to the sink (nil sink discards).
func (t *Thinker) emit(ctx context.Context, kind ChunkKind, text string) {
	if t.sink != nil {
		t.sink(ctx, Chunk{Kind: kind, Text: text})
	}
}
