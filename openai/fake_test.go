// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// fake_test.go — test infrastructure: OpenAI-compatible fake servers for
// both wires plus Thinker constructors. The fakes decode only the fields
// the assertions need (model / stream / tool count) and reply with
// hand-written JSON so the package's own wire structs stay an
// implementation detail, not the test oracle.

package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// fake endpoint paths — the full request paths (client base is ts.URL+"/v1").
const (
	fakeChatPath      = "/v1/chat/completions"
	fakeResponsesPath = "/v1/responses"
)

// recordedRequest is the minimal request projection both fakes record.
type recordedRequest struct {
	Model         string            `json:"model"`
	Stream        bool              `json:"stream"`
	Tools         []json.RawMessage `json:"tools"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

// fakeServer is one wire-agnostic endpoint fake: it records the last
// request and replies either with a JSON body (non-stream) or with SSE
// data frames (stream). The chat wire terminates with "data: [DONE]";
// the responses wire ends on connection close without it.
type fakeServer struct {
	t            *testing.T
	path         string
	mu           sync.Mutex
	stream       bool
	tools        int
	model        string
	includeUsage bool
	chunks       []string // SSE data payloads
	response     string   // non-stream JSON body
}

func (f *fakeServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != f.path {
			http.NotFound(w, r)
			return
		}
		var req recordedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("fakeServer: decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.stream = req.Stream
		f.tools = len(req.Tools)
		f.model = req.Model
		f.includeUsage = req.StreamOptions != nil && req.StreamOptions.IncludeUsage
		chunks, response := f.chunks, f.response
		f.mu.Unlock()
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, c := range chunks {
				fmt.Fprintf(w, "data: %s\n\n", c)
			}
			if f.path == fakeChatPath {
				fmt.Fprint(w, "data: [DONE]\n\n") // chat terminates with [DONE]
			}
			return // the responses wire ends on connection close (EOF)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, response)
	})
}

func (f *fakeServer) lastStream() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stream
}

func (f *fakeServer) lastTools() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools
}

func (f *fakeServer) lastModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.model
}

func (f *fakeServer) lastIncludeUsage() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.includeUsage
}

// newFakeThinker starts the fake and constructs a Thinker against it; opts
// carry the gate/sink/no-tools wiring per test.
func newFakeThinker(t *testing.T, f *fakeServer, opts ...Option) *Thinker {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	th, err := New(Config{
		BaseURL: ts.URL + "/v1",
		Model:   "test-model",
	}, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return th
}

// basePrompt is the minimal Prompt the Think tests ride.
func basePrompt() *meowire.Prompt {
	return &meowire.Prompt{System: "sys", Input: "hi"}
}

// promptWithTools carries one tool definition (the no-tools tests strip it).
func promptWithTools() *meowire.Prompt {
	return &meowire.Prompt{
		System: "sys",
		Input:  "hi",
		Tools:  []meowire.ToolSpec{{Name: "bash", Desc: "run a command"}},
	}
}

// wantUsage builds the expected meowire.Usage value.
func wantUsage(prompt, completion, total int) meowire.Usage {
	return meowire.Usage{Prompt: prompt, Completion: completion, Total: total}
}

// collectSink installs a streaming gate + sink pair and returns the chunk
// recorder (kind/text arrive verbatim; order preserved).
func collectSink() (Option, Option, *[]Chunk) {
	got := &[]Chunk{}
	gate := WithStreamGate(func() bool { return true })
	sink := WithChunkSink(func(_ context.Context, c Chunk) { *got = append(*got, c) })
	return gate, sink, got
}
