// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// brain_test.go — the bundled brain against a scripted endpoint: both
// transports round-trip, the pairing holds, every Prompt field lands, and the
// error/usage shapes stay honest.
package brain_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3"

	"github.com/qyiun666/meowire/internal/brain"
	"github.com/qyiun666/meowire/internal/nerve"
	"github.com/qyiun666/meowire/internal/testutil"
)

// capturedMessage is one message of an arrived request, decoded only as far
// as the assertions need.
type capturedMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// capturedRequest is one arrived /chat/completions request body.
type capturedRequest struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	Messages []capturedMessage `json:"messages"`
	Tools    []struct {
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

// decode parses the n-th recorded request body.
func decode(t *testing.T, bodies []string, n int) capturedRequest {
	t.Helper()
	var req capturedRequest
	if err := json.Unmarshal([]byte(bodies[n]), &req); err != nil {
		t.Fatalf("decode captured request %d: %v", n, err)
	}
	return req
}

// fullPrompt fills all eleven fields.
func fullPrompt() *nerve.Prompt {
	return &nerve.Prompt{
		System:     "be helpful",
		Identity:   "a test agent",
		Methods:    []nerve.MethodSpec{{Name: "m1", Desc: "does one thing"}},
		Tools:      []nerve.ToolSpec{{Name: "search", Desc: "finds things", Input: `{"type":"object","properties":{"q":{"type":"string"}}}`}},
		Context:    []string{"base line", "[sandbox-denied: web is off]"},
		Bounds:     "only /workspace",
		Input:      "work",
		Plan:       "step 1 then step 2",
		Reflection: "last time the tool args were wrong",
		ToolResults: []nerve.ToolResult{
			{ID: "call_1", Name: "search", Result: "42"},
			{ID: "call_2", Name: "web", Err: "boom"},
		},
		Memories: []nerve.Record{{Key: "k1", Kind: "note", Content: []byte("remembered"), Created: 1720000000000}},
	}
}

// TestRoundTripBlocking: one blocking request carries the whole prompt and
// folds the completion back with text, calls and usage.
func TestRoundTripBlocking(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{
		Text:      "the answer",
		ToolCalls: []testutil.FakeCall{{ID: "call_9", Name: "calc", Args: `{"x":1}`}},
		Usage:     testutil.FakeUsage{Prompt: 10, Completion: 5, Total: 15},
	})
	b := brain.New(fb.Cfg(false))

	dec, err := b.Think(context.Background(), fullPrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "the answer" {
		t.Fatalf("text = %q, want the scripted answer", dec.Text)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_9" ||
		dec.ToolCalls[0].Name != "calc" || dec.ToolCalls[0].Args != `{"x":1}` {
		t.Fatalf("tool calls = %+v, want call_9/calc verbatim", dec.ToolCalls)
	}
	if dec.Usage == nil || dec.Usage.Prompt != 10 || dec.Usage.Completion != 5 || dec.Usage.Total != 15 {
		t.Fatalf("usage = %+v, want the scripted accounting", dec.Usage)
	}

	req := decode(t, fb.Requests(), 0)
	if req.Model != "fake-model" || req.Stream {
		t.Fatalf("request model/stream = %q/%v", req.Model, req.Stream)
	}
	if len(req.Tools) != 1 || req.Tools[0].Function.Name != "search" {
		t.Fatalf("request tools = %+v, want the round's tool list", req.Tools)
	}
	if req.Tools[0].Function.Parameters["type"] != "object" {
		t.Fatalf("tool parameters = %+v, want the decoded schema", req.Tools[0].Function.Parameters)
	}
	if !strings.Contains(req.Tools[0].Function.Description, "finds things") {
		t.Fatalf("tool description = %q, want the spec's", req.Tools[0].Function.Description)
	}
}

// TestPromptFieldPlacement: every text field lands in the system bundle, the
// stimulus is the user message, and the conversation order is
// system → user → assistant/tool pairs.
func TestPromptFieldPlacement(t *testing.T) {
	fb := testutil.NewFakeBrain(t)
	b := brain.New(fb.Cfg(false))
	if _, err := b.Think(context.Background(), fullPrompt()); err != nil {
		t.Fatalf("Think: %v", err)
	}
	req := decode(t, fb.Requests(), 0)

	if len(req.Messages) != 5 {
		t.Fatalf("messages = %d, want 5 (system, user, assistant, two tool replies)", len(req.Messages))
	}
	sys := req.Messages[0]
	for _, want := range []string{
		"be helpful", "a test agent", "m1", "only /workspace",
		"base line", "[sandbox-denied: web is off]", "step 1 then step 2",
		"last time the tool args were wrong", "k1", "remembered", "@1720000000000",
	} {
		if !strings.Contains(sys.Content, want) {
			t.Errorf("system bundle missing %q:\n%s", want, sys.Content)
		}
	}
	if sys.Role != "system" {
		t.Fatalf("first message role = %q, want system", sys.Role)
	}
	if req.Messages[1].Role != "user" || req.Messages[1].Content != "work" {
		t.Fatalf("second message = %q/%q, want the stimulus", req.Messages[1].Role, req.Messages[1].Content)
	}
	// The pairing: one assistant message carries both calls, each followed by
	// its tool reply — Err wins over Result on the failed one.
	asst := req.Messages[2]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 2 {
		t.Fatalf("assistant message = %+v, want two calls", asst)
	}
	if asst.ToolCalls[0].ID != "call_1" || asst.ToolCalls[1].ID != "call_2" {
		t.Fatalf("assistant calls = %+v, want the results' echoes in order", asst.ToolCalls)
	}
	if asst.ToolCalls[0].Function.Name != "search" || asst.ToolCalls[1].Function.Name != "web" {
		t.Fatalf("assistant call names = %+v, want the echoes", asst.ToolCalls)
	}
	if req.Messages[3].Role != "tool" || req.Messages[3].ToolCallID != "call_1" || req.Messages[3].Content != "42" {
		t.Fatalf("tool reply 1 = %+v, want call_1/42", req.Messages[3])
	}
	if req.Messages[4].Role != "tool" || req.Messages[4].ToolCallID != "call_2" || req.Messages[4].Content != "boom" {
		t.Fatalf("tool reply 2 = %+v, want call_2/boom (Err wins)", req.Messages[4])
	}
}

// TestStreamingSinkAndToolCalls: the SSE path folds tool calls through the
// accumulator and pushes text deltas to the sink before the round ends.
func TestStreamingSinkAndToolCalls(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{
		Text:      "hello stream",
		ToolCalls: []testutil.FakeCall{{ID: "call_9", Name: "calc", Args: `{"x":1}`}},
		Usage:     testutil.FakeUsage{Prompt: 7, Completion: 3, Total: 10},
	})
	b := brain.New(fb.Cfg(true))

	var deltas []string
	ctx := brain.WithSink(context.Background(), func(delta string) {
		deltas = append(deltas, delta)
	})
	dec, err := b.Think(ctx, fullPrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "hello stream" {
		t.Fatalf("text = %q, want the accumulated stream", dec.Text)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_9" || dec.ToolCalls[0].Args != `{"x":1}` {
		t.Fatalf("streamed tool calls = %+v, want the merged fragment", dec.ToolCalls)
	}
	if dec.Usage == nil || dec.Usage.Total != 10 {
		t.Fatalf("streamed usage = %+v, want the final chunk's", dec.Usage)
	}
	if len(deltas) < 2 || strings.Join(deltas, "") != "hello stream" {
		t.Fatalf("sink deltas = %q, want the text in real increments", deltas)
	}
	req := decode(t, fb.Requests(), 0)
	if !req.Stream {
		t.Fatal("request should ask for the stream transport")
	}
}

// TestAPIErrorShape: a non-2xx answer arrives wrapped, with the status in the
// text, and errors.As still reaches the SDK's shape through %w.
func TestAPIErrorShape(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{ErrStatus: 400})
	b := brain.New(fb.Cfg(false))

	_, err := b.Think(context.Background(), fullPrompt())
	if err == nil {
		t.Fatal("a rejected completion must be an error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %v, want the status in the text (fail fast on permanent errors)", err)
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 {
		t.Fatalf("errors.As = %v, want the SDK's *openai.Error reachable", err)
	}
}

// TestUsageZeroOmitted: a wire with zero usage yields a nil Usage — the event
// stream then reports nothing instead of a fake accounting.
func TestUsageZeroOmitted(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Text: "t"})
	b := brain.New(fb.Cfg(false))
	dec, err := b.Think(context.Background(), &nerve.Prompt{Input: "x"})
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Usage != nil {
		t.Fatalf("usage = %+v, want nil on an all-zero wire", dec.Usage)
	}
}

// TestNoToolsOmitsField: a round without tools sends no tools field on either
// wire — nil, which the wire omits, not an empty array, which some
// OpenAI-compatible endpoints reject. Pinned on the raw request bodies.
func TestNoToolsOmitsField(t *testing.T) {
	p := &nerve.Prompt{Input: "x"}
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Text: "t"})
	if _, err := brain.New(fb.Cfg(false)).Think(context.Background(), p); err != nil {
		t.Fatalf("chat Think: %v", err)
	}
	if _, err := brain.New(fb.CfgResponses(false)).Think(context.Background(), p); err != nil {
		t.Fatalf("responses Think: %v", err)
	}
	bodies := fb.Requests()
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2 (one per wire)", len(bodies))
	}
	for i, body := range bodies {
		if strings.Contains(body, `"tools"`) {
			t.Fatalf("request %d carries a tools field: %s", i, body)
		}
	}
}

// TestBadToolSchemaFailsLoudly: a malformed input schema is a wiring mistake;
// it errors before any request leaves.
func TestBadToolSchemaFailsLoudly(t *testing.T) {
	fb := testutil.NewFakeBrain(t)
	b := brain.New(fb.Cfg(false))
	p := &nerve.Prompt{
		Input: "x",
		Tools: []nerve.ToolSpec{{Name: "broken", Input: "not-json{"}},
	}
	_, err := b.Think(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("error = %v, want the tool named and the reason", err)
	}
	if fb.Hits() != 0 {
		t.Fatalf("requests = %d, want none (the failure is pre-flight)", fb.Hits())
	}
}

// capturedResponsesRequest is one arrived /responses request body, decoded
// only as far as the assertions need.
type capturedResponsesRequest struct {
	Model        string                  `json:"model"`
	Instructions string                  `json:"instructions"`
	Store        *bool                   `json:"store"`
	Stream       bool                    `json:"stream"`
	Input        []capturedResponsesItem `json:"input"`
	Tools        []capturedResponsesTool `json:"tools"`
}

// capturedResponsesTool is one arrived function tool declaration.
type capturedResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// capturedResponsesItem is one input item: a message (role + content), a
// function call (call_id + name + arguments), or a call output (call_id +
// output). The content and output unions inline their string variant, so
// they arrive as raw JSON and the assertions unquote what they need.
type capturedResponsesItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// decodeResponses parses the n-th recorded /responses request body.
func decodeResponses(t *testing.T, bodies []string, n int) capturedResponsesRequest {
	t.Helper()
	var req capturedResponsesRequest
	if err := json.Unmarshal([]byte(bodies[n]), &req); err != nil {
		t.Fatalf("decode captured responses request %d: %v", n, err)
	}
	return req
}

// rawString unquotes a string-variant union member ("" when absent).
func rawString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("union member %s is not the string variant", raw)
	}
	return s
}

// TestResponsesRoundTrip: one blocking request on the responses wire carries
// the whole prompt — instructions, stimulus, rebuilt call pairs, tools, and
// store off — and folds the run back with text, calls and usage. The second
// scripted answer carries no usage: the fold leaves Usage nil instead of a
// fake accounting.
func TestResponsesRoundTrip(t *testing.T) {
	fb := testutil.NewFakeBrain(t,
		testutil.FakeCompletion{
			Text:      "the answer",
			ToolCalls: []testutil.FakeCall{{ID: "call_9", Name: "calc", Args: `{"x":1}`}},
			Usage:     testutil.FakeUsage{Prompt: 10, Completion: 5, Total: 15},
		},
		testutil.FakeCompletion{Text: "bare"},
	)
	b := brain.New(fb.CfgResponses(false))

	dec, err := b.Think(context.Background(), fullPrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "the answer" {
		t.Fatalf("text = %q, want the scripted answer", dec.Text)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_9" ||
		dec.ToolCalls[0].Name != "calc" || dec.ToolCalls[0].Args != `{"x":1}` {
		t.Fatalf("tool calls = %+v, want call_9/calc verbatim", dec.ToolCalls)
	}
	if dec.Usage == nil || dec.Usage.Prompt != 10 || dec.Usage.Completion != 5 || dec.Usage.Total != 15 {
		t.Fatalf("usage = %+v, want the scripted accounting", dec.Usage)
	}

	req := decodeResponses(t, fb.Requests(), 0)
	if req.Model != "fake-model" || req.Stream {
		t.Fatalf("request model/stream = %q/%v", req.Model, req.Stream)
	}
	if req.Store == nil || *req.Store {
		t.Fatalf("store = %v, want an explicit false (the kernel rebuilds, it never reads state back)", req.Store)
	}
	for _, want := range []string{
		"be helpful", "a test agent", "m1", "only /workspace",
		"base line", "[sandbox-denied: web is off]", "step 1 then step 2",
		"last time the tool args were wrong", "k1", "remembered", "@1720000000000",
	} {
		if !strings.Contains(req.Instructions, want) {
			t.Errorf("instructions missing %q:\n%s", want, req.Instructions)
		}
	}
	// The input: the stimulus first (an easy input message carries no type
	// discriminator on the wire — role + content say message), then one call
	// + output pair per result, Err winning on the failed one.
	if len(req.Input) != 5 {
		t.Fatalf("input items = %d, want 5 (the stimulus, two pairs)", len(req.Input))
	}
	if req.Input[0].Role != "user" || rawString(t, req.Input[0].Content) != "work" {
		t.Fatalf("first item = %+v, want the stimulus", req.Input[0])
	}
	if req.Input[1].Type != "function_call" || req.Input[1].CallID != "call_1" || req.Input[1].Name != "search" {
		t.Fatalf("pair call 1 = %+v, want the result's echo", req.Input[1])
	}
	if req.Input[2].Type != "function_call_output" || req.Input[2].CallID != "call_1" || rawString(t, req.Input[2].Output) != "42" {
		t.Fatalf("pair output 1 = %+v, want call_1/42", req.Input[2])
	}
	if req.Input[3].Type != "function_call" || req.Input[3].CallID != "call_2" || req.Input[3].Name != "web" {
		t.Fatalf("pair call 2 = %+v, want the result's echo", req.Input[3])
	}
	if req.Input[4].Type != "function_call_output" || req.Input[4].CallID != "call_2" || rawString(t, req.Input[4].Output) != "boom" {
		t.Fatalf("pair output 2 = %+v, want call_2/boom (Err wins)", req.Input[4])
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "search" || req.Tools[0].Type != "function" {
		t.Fatalf("request tools = %+v, want the round's tool list", req.Tools)
	}
	if req.Tools[0].Parameters["type"] != "object" {
		t.Fatalf("tool parameters = %+v, want the decoded schema", req.Tools[0].Parameters)
	}
	if !strings.Contains(req.Tools[0].Description, "finds things") {
		t.Fatalf("tool description = %q, want the spec's", req.Tools[0].Description)
	}

	dec, err = b.Think(context.Background(), fullPrompt())
	if err != nil {
		t.Fatalf("Think 2: %v", err)
	}
	if dec.Text != "bare" || dec.Usage != nil {
		t.Fatalf("second fold = %q/%+v, want the bare text and no usage", dec.Text, dec.Usage)
	}
}

// TestResponsesStreaming: the SSE path pushes text deltas to the sink, and
// the fold reads the terminal event's whole response — calls and usage
// arrive inside response.completed, not as fragments to merge.
func TestResponsesStreaming(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{
		Text:      "hello stream",
		ToolCalls: []testutil.FakeCall{{ID: "call_9", Name: "calc", Args: `{"x":1}`}},
		Usage:     testutil.FakeUsage{Prompt: 7, Completion: 3, Total: 10},
	})
	b := brain.New(fb.CfgResponses(true))

	var deltas []string
	ctx := brain.WithSink(context.Background(), func(delta string) {
		deltas = append(deltas, delta)
	})
	dec, err := b.Think(ctx, fullPrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "hello stream" {
		t.Fatalf("text = %q, want the terminal event's", dec.Text)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_9" || dec.ToolCalls[0].Args != `{"x":1}` {
		t.Fatalf("streamed tool calls = %+v, want the completed response's", dec.ToolCalls)
	}
	if dec.Usage == nil || dec.Usage.Total != 10 {
		t.Fatalf("streamed usage = %+v, want the completed response's", dec.Usage)
	}
	if len(deltas) < 2 || strings.Join(deltas, "") != "hello stream" {
		t.Fatalf("sink deltas = %q, want the text in real increments", deltas)
	}
	req := decodeResponses(t, fb.Requests(), 0)
	if !req.Stream {
		t.Fatal("request should ask for the stream transport")
	}
}

// TestResponsesFailedState: a terminal failure folds into an error with the
// code in the text, not an empty decision.
func TestResponsesFailedState(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Status: "failed"})
	b := brain.New(fb.CfgResponses(false))

	_, err := b.Think(context.Background(), fullPrompt())
	if err == nil {
		t.Fatal("a failed responses run must be an error")
	}
	if !strings.Contains(err.Error(), "scripted_failure") {
		t.Fatalf("error = %v, want the failure code in the text", err)
	}
}

// TestModeSelectsWire: the mode routes the request — the zero value and
// ModeChat take the chat wire, ModeResponses takes the responses wire.
func TestModeSelectsWire(t *testing.T) {
	fb := testutil.NewFakeBrain(t)
	for _, mode := range []brain.Mode{0, brain.ModeChat, brain.ModeResponses} {
		cfg := fb.Cfg(false)
		cfg.Mode = mode
		b := brain.New(cfg)
		if _, err := b.Think(context.Background(), &nerve.Prompt{Input: "x"}); err != nil {
			t.Fatalf("Think (mode %d): %v", mode, err)
		}
	}
	paths := fb.Paths()
	want := []string{"/chat/completions", "/chat/completions", "/responses"}
	if len(paths) != 3 || paths[0] != want[0] || paths[1] != want[1] || paths[2] != want[2] {
		t.Fatalf("paths = %q, want %q (mode routing)", paths, want)
	}
}

// TestResponsesIncompleteKeepsPartialText: an incomplete terminal state is
// not a failure — the fold returns what the run produced, the same way the
// chat wire lets a length-cut finish through.
func TestResponsesIncompleteKeepsPartialText(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Status: "incomplete", Text: "partial"})
	b := brain.New(fb.CfgResponses(false))
	dec, err := b.Think(context.Background(), fullPrompt())
	if err != nil {
		t.Fatalf("Think: %v (incomplete is not a failure)", err)
	}
	if dec.Text != "partial" {
		t.Fatalf("text = %q, want the partial output kept", dec.Text)
	}
}

// TestResponsesStreamingFailedTerminal: the failed terminal event folds into
// an error on the streaming path, exactly as the blocking one does.
func TestResponsesStreamingFailedTerminal(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Status: "failed"})
	b := brain.New(fb.CfgResponses(true))
	_, err := b.Think(context.Background(), fullPrompt())
	if err == nil || !strings.Contains(err.Error(), "scripted_failure") {
		t.Fatalf("error = %v, want the failed terminal event as an error", err)
	}
}

// TestResponsesStreamErrorEvent: a mid-stream error event ends the round as
// an error (the deferred Close keeps the early return from leaking the
// connection; this pins the branch itself).
func TestResponsesStreamErrorEvent(t *testing.T) {
	fb := testutil.NewFakeBrain(t, testutil.FakeCompletion{Text: "x", StreamError: true})
	b := brain.New(fb.CfgResponses(true))
	_, err := b.Think(context.Background(), fullPrompt())
	if err == nil || !strings.Contains(err.Error(), "scripted stream error") {
		t.Fatalf("error = %v, want the stream error event as an error", err)
	}
}

// TestResponsesStreamNoTerminal: a stream that ends cleanly without any
// terminal event is an error, not a silently empty decision.
func TestResponsesStreamNoTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"type":"response.output_text.delta","delta":"orphan"}`)
	}))
	t.Cleanup(srv.Close)
	b := brain.New(brain.Config{
		BaseURL: srv.URL, Key: "test-value-not-a-credential", Model: "m",
		Stream: true, Mode: brain.ModeResponses,
	})
	_, err := b.Think(context.Background(), &nerve.Prompt{Input: "x"})
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("error = %v, want the missing-terminal-event error", err)
	}
}
