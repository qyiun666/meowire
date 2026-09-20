// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// brain.go — the bundled brain: the one organ the framework ships. It turns a
// nerve.Prompt into one LLM call on either wire the mode enum selects — chat
// completions (the default) or the Responses API — and folds it back into a
// nerve.Decision. The loop still sees a Thinker and the openai SDK never
// leaves this package — provider vocabulary stops here, at the composition
// root's one organ.
package brain

import (
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/qyiun666/meowire/internal/nerve"
)

// Mode selects the wire the brain speaks: the chat-completions protocol (the
// default — the zero value lands there too, so an assembly that never sets a
// Mode still gets chat) or the Responses API. Both are stateless renders of
// the same Prompt into the same Decision; the mode is a transport choice,
// not a contract fork.
type Mode int

const (
	ModeChat      Mode = 1 // chat completions (default)
	ModeResponses Mode = 2 // the Responses API wire
)

// Config is the brain's assembly parameters (the public BrainConfig).
// BaseURL empty means the official endpoint; Model and Key have no defaults,
// and the composition root's Validate rejects their absence — and a Mode
// outside the enum — at assembly time; this package receives values it can
// use and does not re-check them.
type Config struct {
	BaseURL string // OpenAI-compatible endpoint ("" = the official one)
	Key     string // credential sent as the bearer token
	Model   string // model id, sent per request
	Stream  bool   // true = SSE transport, text deltas reach the Sink
	Mode    Mode   // wire selector: ModeChat (default, zero value included) or ModeResponses
}

// Brain is the bundled Thinker. It is stateless: one Prompt is the whole
// truth of a round, so the same Brain answers concurrent Stimulates without
// locks (the SDK client is safe for concurrent use).
type Brain struct {
	client openai.Client
	model  openai.ChatModel
	stream bool
	mode   Mode
}

// The compile-time pin: the loop's port and this organ agree by compiler,
// not by convention.
var _ nerve.Thinker = (*Brain)(nil)

// New builds the brain. Transport retries are off here: the one retry budget
// is the loop's Config.MaxRetries, and two layers would multiply one failure
// into MaxRetries × SDK-retries requests.
func New(cfg Config) *Brain {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.Key),
		option.WithMaxRetries(0),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &Brain{
		client: openai.NewClient(opts...),
		model:  openai.ChatModel(cfg.Model),
		stream: cfg.Stream,
		mode:   cfg.Mode,
	}
}

// Think renders the Prompt into one LLM call — chat completions or the
// Responses wire, per the mode — and folds the answer back into a Decision.
// Tool-call arguments pass through verbatim: judging them is the Effector's
// and the membrane's business, and a brain that rewrote them would turn the
// model's intent into the host's.
func (b *Brain) Think(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
	if b.mode == ModeResponses {
		return b.thinkResponses(ctx, p)
	}
	tools, err := toolParams(p)
	if err != nil {
		return nil, err
	}
	params := openai.ChatCompletionNewParams{
		Messages: messages(p),
		Model:    b.model,
		Tools:    tools,
	}
	if b.stream {
		completion, err := streamCompletion(ctx, b.client, params, sinkFrom(ctx))
		if err != nil {
			return nil, err
		}
		return decisionOf(completion)
	}
	completion, err := b.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return nil, wrapErr(err, "chat completion")
	}
	return decisionOf(completion)
}

// wrapErr adds context to a transport failure, naming the wire that was
// refused. Permanent rejections carry their status in the text so the round
// can fail fast instead of burning the loop's retry budget: this port does
// not retry, the loop does. The SDK wraps non-2xx as *openai.Error and passes
// network errors through unwrapped, so errors.As still reaches the SDK shape
// through %w.
func wrapErr(err error, wire string) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("brain: %s rejected (status %d, code %q): %w",
			wire, apiErr.StatusCode, apiErr.Code, err)
	}
	return fmt.Errorf("brain: %s request failed: %w", wire, err)
}

// Sink receives the model's text deltas during a streaming Think. It is the
// host's channel, not part of the event stream: the only text event is
// EventText, whole and already past the output membrane. Deltas reach the
// sink before that ruling, so a host showing them live replaces the whole
// segment when EventText arrives (stream-and-correct).
type Sink func(delta string)

type sinkKey struct{}

// WithSink mounts the streaming channel on the call's context — never a
// field, or concurrent Thinks would cross-talk.
func WithSink(ctx context.Context, sink Sink) context.Context {
	return context.WithValue(ctx, sinkKey{}, sink)
}

func sinkFrom(ctx context.Context) Sink {
	sink, _ := ctx.Value(sinkKey{}).(Sink)
	return sink
}
