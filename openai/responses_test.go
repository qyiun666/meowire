// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// responses_test.go — Responses API wire tests (wire detection / request
// rendering / non-stream parsing / stream event parsing / no-tools).

package openai

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- hand-written wire payloads (independent JSON oracle) ----

const (
	respTextOK = `{"status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"你好，世界"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`

	respToolOK = `{"status":"completed","output":[{"id":"fc_1","call_id":"call_1","type":"function_call","status":"completed","name":"calc","arguments":"{\"expr\":\"1+1\"}"}]}`

	respFailed = `{"status":"failed","error":{"code":"server_error","message":"boom"}}`

	respEmpty = `{"status":"completed","output":[]}`

	respLight = `{"status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"轻量回复"}]}]}`

	evTextA           = `{"type":"response.output_text.delta","delta":"你好"}`
	evTextB           = `{"type":"response.output_text.delta","delta":"世界"}`
	evReason          = `{"type":"response.reasoning_text.delta","delta":"思考中"}`
	evArgsA           = `{"type":"response.function_call_arguments.delta","output_index":0,"arguments":"{\"expr\":"}`
	evArgsB           = `{"type":"response.function_call_arguments.delta","output_index":0,"arguments":"\"1+1\"}"}`
	evItemAdd         = `{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","call_id":"call_1","type":"function_call","name":"calc"}}`
	evItemDonePayload = `{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","call_id":"call_1","type":"function_call","name":"calc","arguments":"{\"expr\":\"1+1\"}"}}`
	evDone            = `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`
	evBroke           = `{"type":"response.error","message":"stream broke"}`
)

// stream edge-case fixtures
const (
	evIncompletePayload = `{"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}}`
	evItemAddNoIdx      = `{"type":"response.output_item.added","item":{"id":"fc_1","call_id":"call_1","type":"function_call","name":"calc"}}`
	evArgsNoIdx         = `{"type":"response.function_call_arguments.delta","arguments":"{\"expr\":"}`
)

// ---- wire detection ----

func TestWireFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want Wire
	}{
		{"https://api.deepseek.com", WireResponses},
		{"https://api.deepseek.com/v1", WireResponses},
		{"https://api.deepseek.com/", WireResponses},
		{"https://api.openai.com/v1", WireChat},
		{"https://dashscope.aliyuncs.com/compatible-mode/v1", WireChat},
		{"http://localhost:11434/v1", WireChat},
		{"", WireChat},
	}
	for _, c := range cases {
		if got := WireFromURL(c.url); got != c.want {
			t.Errorf("WireFromURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

// ---- once path ----

func TestRespOnce_TextAndToolCall(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respTextOK}
	th := newFakeThinker(t, f, WithWire(WireResponses))

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好，世界" {
		t.Errorf("Text = %q, want 你好，世界", dec.Text)
	}
	if len(dec.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %+v, want empty", dec.ToolCalls)
	}
	if dec.Usage == nil || *dec.Usage != wantUsage(10, 5, 15) {
		t.Errorf("Usage = %+v, want 10/5/15", dec.Usage)
	}
	if f.lastStream() {
		t.Error("no gate should mean a non-stream request")
	}
}

func TestRespOnce_ToolCall(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respToolOK}
	th := newFakeThinker(t, f, WithWire(WireResponses))

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_1" || dec.ToolCalls[0].Name != "calc" || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want call_1/calc (ID prefers call_id)", dec.ToolCalls)
	}
	if dec.Usage != nil {
		t.Errorf("Usage = %+v, want nil", dec.Usage)
	}
}

func TestRespOnce_FailedStatus(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respFailed}
	th := newFakeThinker(t, f, WithWire(WireResponses))

	if _, err := th.Think(context.Background(), basePrompt()); err == nil {
		t.Fatal("status=failed must error")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error must carry the server message, got %v", err)
	}
}

func TestRespOnce_EmptyOutput(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respEmpty}
	th := newFakeThinker(t, f, WithWire(WireResponses))

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "" || len(dec.ToolCalls) != 0 {
		t.Errorf("empty output must yield an empty decision, got %+v", dec)
	}
}

// ---- stream path ----

func TestRespStream_TextAndToolCall(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evTextA, evTextB, evItemAdd, evArgsA, evArgsB, evItemDonePayload, evDone}}
	gate, sink, got := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好世界" {
		t.Errorf("Text = %q, want 你好世界", dec.Text)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_1" || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want reassembled call_1", dec.ToolCalls)
	}
	if dec.Usage == nil || *dec.Usage != wantUsage(10, 5, 15) {
		t.Errorf("Usage = %+v, want 10/5/15", dec.Usage)
	}
	if !f.lastStream() {
		t.Error("gate hit should mean a stream request")
	}
	for i, want := range []string{"你好", "世界"} {
		if (*got)[i].Kind != ChunkText || (*got)[i].Text != want {
			t.Errorf("chunk %d = %+v, want ChunkText %q", i, (*got)[i], want)
		}
	}
}

// TestRespStream_DoneOverridesDeltas the output_item.done payload carries
// the complete arguments — it must win over whatever accumulated (some
// gateways skip the per-segment deltas).
func TestRespStream_DoneOverridesDeltas(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evItemAdd, evArgsA, evArgsB, evItemDonePayload, evDone}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want done-payload arguments", dec.ToolCalls)
	}
}

func TestRespStream_ReasoningFramedSeparately(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evReason, evTextA, evDone}}
	gate, sink, got := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好" {
		t.Errorf("Text = %q, want 你好 (reasoning must not mix into the body)", dec.Text)
	}
	if len(*got) != 2 || (*got)[0].Kind != ChunkReasoning || (*got)[1].Kind != ChunkText {
		t.Errorf("chunks = %+v, want [ChunkReasoning ChunkText]", *got)
	}
}

func TestRespStream_FailedEvent(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evTextA, evBroke}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	if _, err := th.Think(context.Background(), basePrompt()); err == nil {
		t.Fatal("an error event must terminate with an error")
	}
}

// TestRespStream_IncompleteCarriesUsage abnormal terminations are not
// silenced: the error text carries the accounting seen so far (the Thinker
// port has no usage-on-error channel, so the error text is the delivery).
func TestRespStream_IncompleteCarriesUsage(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evTextA, evIncompletePayload}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	_, err := th.Think(context.Background(), basePrompt())
	if err == nil {
		t.Fatal("an incomplete event must terminate with an error")
	}
	if !strings.Contains(err.Error(), "incomplete") || !strings.Contains(err.Error(), "7 prompt + 3 completion") {
		t.Errorf("error = %v, want the event type and the usage seen so far", err)
	}
}

// TestRespStream_MissingOutputIndex events without output_index must not
// lose the tool call — they degrade to the single slot 0 (the done payload
// with explicit index 0 then lands its complete arguments on the same slot).
func TestRespStream_MissingOutputIndex(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, chunks: []string{evItemAddNoIdx, evArgsNoIdx, evItemDonePayload}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, WithWire(WireResponses), gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want slot-0 accumulation with the done override", dec.ToolCalls)
	}
}

// ---- dispatch & no-tools ----

func TestThink_WireExplicit(t *testing.T) {
	// the explicit Wire wins over the URL detection: with no gate the
	// request must land on the responses path
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respLight}
	th := newFakeThinker(t, f, WithWire(WireResponses))
	if _, err := th.Think(context.Background(), basePrompt()); err != nil {
		t.Fatalf("Think: %v", err)
	}
	if f.lastModel() != "test-model" {
		t.Errorf("Model = %q, want test-model", f.lastModel())
	}
}

func TestThink_ResponsesNoTools(t *testing.T) {
	f := &fakeServer{t: t, path: fakeResponsesPath, response: respLight}
	th := newFakeThinker(t, f, WithWire(WireResponses), WithNoTools())

	dec, err := th.Think(context.Background(), promptWithTools())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "轻量回复" {
		t.Errorf("Text = %q, want 轻量回复", dec.Text)
	}
	if n := f.lastTools(); n != 0 {
		t.Errorf("no-tools request carried %d tool definitions, want 0", n)
	}
}

// WithWire pins Config.Wire (test convenience mirroring the field).
func WithWire(w Wire) Option {
	return func(t *Thinker) { t.cfg.Wire = w }
}

// startFake is newFakeThinker's server half for tests that build their own
// Config.
func startFake(t *testing.T, f *fakeServer) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(f.handler())
	t.Cleanup(ts.Close)
	return ts
}
