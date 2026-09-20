// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// fakebrain.go — a scripted OpenAI-compatible endpoint pair for offline
// tests: it serves a fixed sequence of answers on both wires the brain can
// select (/chat/completions and /responses), answers streaming requests as
// SSE, records every request path and body, and never leaves the process.
package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/qyiun666/meowire/internal/brain"
)

// FakeCall is one scripted tool call.
type FakeCall struct {
	ID   string
	Name string
	Args string
}

// FakeUsage is one scripted token accounting.
type FakeUsage struct {
	Prompt     int
	Completion int
	Total      int
}

// FakeCompletion is one scripted answer: text, tool calls and usage — or,
// with ErrStatus non-zero, an HTTP failure carrying that status — or, with
// Status non-empty (responses wire only), a terminal state ("failed" must
// fold into an error, "incomplete" keeps its partial output) — or, with
// StreamError true (responses wire only), a mid-stream error event that
// ends the stream without any terminal state.
type FakeCompletion struct {
	Text        string
	ToolCalls   []FakeCall
	Usage       FakeUsage
	ErrStatus   int
	Status      string
	StreamError bool
}

// FakeBrain answers POST /chat/completions and POST /responses from its
// script (the same scripted answer, rendered on whichever wire the request
// arrived).
type FakeBrain struct {
	server *httptest.Server

	mu     sync.Mutex
	script []FakeCompletion
	served int
	bodies []string
	paths  []string
	onHit  func()
}

// NewFakeBrain starts the server (closed by t.Cleanup) and installs the
// script. An empty script answers every request with a plain "ok" text.
func NewFakeBrain(t *testing.T, script ...FakeCompletion) *FakeBrain {
	t.Helper()
	if len(script) == 0 {
		script = []FakeCompletion{{Text: "ok"}}
	}
	fb := &FakeBrain{script: script}
	fb.server = httptest.NewServer(http.HandlerFunc(fb.serve))
	t.Cleanup(fb.server.Close)
	return fb
}

// OnHit registers a callback fired on every arriving request (organ-order
// assertions live on it). Register before the first Stimulate.
func (fb *FakeBrain) OnHit(fn func()) {
	fb.mu.Lock()
	fb.onHit = fn
	fb.mu.Unlock()
}

// Cfg returns the brain assembly parameters aimed at this server on the
// default (chat) wire.
func (fb *FakeBrain) Cfg(stream bool) brain.Config {
	return brain.Config{
		BaseURL: fb.server.URL,
		Key:     "test-value-not-a-credential",
		Model:   "fake-model",
		Stream:  stream,
	}
}

// CfgResponses aims the brain assembly parameters at this server on the
// responses wire.
func (fb *FakeBrain) CfgResponses(stream bool) brain.Config {
	cfg := fb.Cfg(stream)
	cfg.Mode = brain.ModeResponses
	return cfg
}

// Hits reports how many requests have arrived.
func (fb *FakeBrain) Hits() int {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.served
}

// Requests returns the raw request bodies in arrival order.
func (fb *FakeBrain) Requests() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.bodies...)
}

// Paths returns the request paths in arrival order — the mode-selection
// assertions' evidence (which wire each request took).
func (fb *FakeBrain) Paths() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.paths...)
}

// serve answers one request from the script (last entry repeats), rendering
// the scripted answer on the wire the request's path selected.
func (fb *FakeBrain) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var respond func(w http.ResponseWriter, c FakeCompletion, stream bool)
	switch r.URL.Path {
	case "/chat/completions":
		respond = respondChat
	case "/responses":
		respond = respondResponses
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	fb.mu.Lock()
	seq := fb.served
	fb.served++
	fb.bodies = append(fb.bodies, string(raw))
	fb.paths = append(fb.paths, r.URL.Path)
	onHit := fb.onHit
	fb.mu.Unlock()
	if onHit != nil {
		onHit()
	}
	if seq >= len(fb.script) {
		seq = len(fb.script) - 1
	}
	c := fb.script[seq]

	if c.ErrStatus != 0 {
		writeErr(w, c.ErrStatus)
		return
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &req) // an undecodable body is the transport's problem
	respond(w, c, req.Stream)
}

// respondChat renders one scripted answer on the chat-completions wire.
func respondChat(w http.ResponseWriter, c FakeCompletion, stream bool) {
	if stream {
		writeSSE(w, c)
		return
	}
	writeJSONCompletion(w, c)
}

// respondResponses renders one scripted answer on the responses wire.
func respondResponses(w http.ResponseWriter, c FakeCompletion, stream bool) {
	if stream {
		writeResponsesSSE(w, c)
		return
	}
	writeResponsesJSON(w, c)
}

// writeJSONCompletion answers one whole completion.
func writeJSONCompletion(w http.ResponseWriter, c FakeCompletion) {
	message := map[string]any{"role": "assistant", "content": c.Text}
	if len(c.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(c.ToolCalls))
		for _, tc := range c.ToolCalls {
			calls = append(calls, map[string]any{
				"id":       tc.ID,
				"type":     "function",
				"function": map[string]string{"name": tc.Name, "arguments": tc.Args},
			})
		}
		message["tool_calls"] = calls
	}
	finish := "stop"
	if len(c.ToolCalls) > 0 {
		finish = "tool_calls"
	}
	payload := map[string]any{
		"id": "cmpl-fake", "object": "chat.completion", "created": 1, "model": "fake-model",
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": finish}},
		"usage": map[string]int{
			"prompt_tokens":     c.Usage.Prompt,
			"completion_tokens": c.Usage.Completion,
			"total_tokens":      c.Usage.Total,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

// writeErr answers one API failure (non-2xx) the SDK will surface as
// *openai.Error.
func writeErr(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"message": "scripted failure", "type": "invalid_request_error", "code": "test_code",
	}})
}

// writeSSE answers the same script as a chunk stream: role and text deltas
// (the text split in two, so a sink sees real increments), one delta per tool
// call, the finish marker, the usage-only final chunk, then [DONE].
func writeSSE(w http.ResponseWriter, c FakeCompletion) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	emit := func(payload map[string]any) {
		line, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	chunk := func(delta map[string]any, finish string) map[string]any {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		return map[string]any{
			"id": "cmpl-fake", "object": "chat.completion.chunk", "model": "fake-model",
			"choices": []any{choice},
		}
	}

	if c.Text != "" {
		runes := []rune(c.Text)
		mid := len(runes) / 2
		emit(chunk(map[string]any{"role": "assistant", "content": string(runes[:mid])}, ""))
		emit(chunk(map[string]any{"content": string(runes[mid:])}, ""))
	} else {
		emit(chunk(map[string]any{"role": "assistant"}, ""))
	}
	for i, tc := range c.ToolCalls {
		emit(chunk(map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": i, "id": tc.ID, "type": "function",
				"function": map[string]string{"name": tc.Name, "arguments": tc.Args},
			}},
		}, ""))
	}
	emit(chunk(map[string]any{}, "stop"))
	emit(map[string]any{
		"id": "cmpl-fake", "object": "chat.completion.chunk", "model": "fake-model",
		"choices": []any{},
		"usage": map[string]int{
			"prompt_tokens":     c.Usage.Prompt,
			"completion_tokens": c.Usage.Completion,
			"total_tokens":      c.Usage.Total,
		},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// writeResponsesJSON answers one whole Responses API run: the response body
// as the blocking payload, terminal state included.
func writeResponsesJSON(w http.ResponseWriter, c FakeCompletion) {
	status := c.Status
	if status == "" {
		status = "completed"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(responsesBody(c, status))
}

// responsesBody renders one Response object: status, output, usage. Only a
// failure is an error run — an incomplete one keeps whatever output it
// produced, which is exactly the distinction the fold has to honor.
func responsesBody(c FakeCompletion, status string) map[string]any {
	body := map[string]any{
		"id": "resp-fake", "object": "response", "created_at": 1, "model": "fake-model",
		"status": status,
		"output": responsesOutput(c),
		"usage": map[string]int{
			"input_tokens":  c.Usage.Prompt,
			"output_tokens": c.Usage.Completion,
			"total_tokens":  c.Usage.Total,
		},
	}
	if status == "failed" {
		body["error"] = map[string]any{
			"code": "scripted_failure", "message": "scripted terminal state",
		}
		body["output"] = []any{}
	}
	return body
}

// responsesOutput renders the scripted answer as response output items.
func responsesOutput(c FakeCompletion) []any {
	var output []any
	if c.Text != "" {
		output = append(output, map[string]any{
			"type": "message", "id": "msg-fake", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{
				"type": "output_text", "text": c.Text, "annotations": []any{},
			}},
		})
	}
	for _, tc := range c.ToolCalls {
		output = append(output, map[string]any{
			"type": "function_call", "id": "fc-fake", "call_id": tc.ID,
			"name": tc.Name, "arguments": tc.Args, "status": "completed",
		})
	}
	return output
}

// writeResponsesSSE answers the same script as a Responses event stream: the
// text split into two output_text deltas (so a sink sees real increments),
// then the terminal event the script calls for — completed carrying the
// whole response (calls and usage included, the fold's only other input) or
// failed carrying the failure — or, with StreamError, a bare error event
// that ends the stream with no terminal state at all.
func writeResponsesSSE(w http.ResponseWriter, c FakeCompletion) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	emit := func(payload map[string]any) {
		line, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", line)
		flusher.Flush()
	}
	if c.StreamError {
		emit(map[string]any{
			"type": "error", "code": "scripted_failure", "message": "scripted stream error",
		})
		return
	}
	if c.Text != "" {
		runes := []rune(c.Text)
		mid := len(runes) / 2
		emit(map[string]any{"type": "response.output_text.delta", "delta": string(runes[:mid])})
		emit(map[string]any{"type": "response.output_text.delta", "delta": string(runes[mid:])})
	}
	terminal, status := "response.completed", c.Status
	if status == "" {
		status = "completed"
	} else if status == "failed" {
		terminal = "response.failed"
	}
	emit(map[string]any{"type": terminal, "response": responsesBody(c, status)})
}
