// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// chat_test.go — chat-wire Think tests (httptest fake, no real LLM):
// entry dispatch / both paths / cancel / chunk framing. Rendering unit
// tests live in render_test.go, transport units in transport_test.go.

package openai

import (
	"context"
	"testing"
	"time"
)

// ---- hand-written wire payloads (independent JSON oracle) ----

const chatTextResp = `{"choices":[{"message":{"role":"assistant","content":"你好，世界"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

const chatToolResp = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"calc","arguments":"{\"expr\":\"1+1\"}"}}]}}]}`

const chatEmptyResp = `{"choices":[{"message":{"role":"assistant","content":"轻量回复"}}]}`

const (
	chunkText   = `{"choices":[{"delta":{"content":"你好"}}]}`
	chunkText2  = `{"choices":[{"delta":{"content":"世界"}}]}`
	chunkReaso  = `{"choices":[{"delta":{"reasoning_content":"思考中"}}]}`
	chunkUsage  = `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	chunkUsage2 = `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	// all-zero accounting: the server shipped a usage chunk with no numbers
	chunkUsageZero = `{"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`
	chunkToolA     = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"calc","arguments":"{\"expr\":"}}]}}]}`
	chunkToolB     = `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"1+1\"}"}}]}}]}`
	// no-index form: gateways that omit delta.index (arrival-order fallback)
	chunkToolNoIdxA = `{"choices":[{"delta":{"tool_calls":[{"id":"call_x","type":"function","function":{"name":"calc","arguments":"{\"expr\":"}}]}}]}`
	chunkToolNoIdxB = `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"2+2\"}"}}]}}]}`
)

// ---- once path ----

func TestChatOnce_Text(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, response: chatTextResp}
	th := newFakeThinker(t, f) // no gate → single-shot

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好，世界" {
		t.Errorf("Text = %q, want 你好，世界", dec.Text)
	}
	if len(dec.ToolCalls) != 0 {
		t.Errorf("ToolCalls = %v, want empty", dec.ToolCalls)
	}
	if dec.Usage == nil || *dec.Usage != (wantUsage(10, 5, 15)) {
		t.Errorf("Usage = %+v, want 10/5/15", dec.Usage)
	}
	if f.lastStream() {
		t.Error("no gate should mean a non-stream request")
	}
	if f.lastModel() != "test-model" {
		t.Errorf("Model = %q, want test-model", f.lastModel())
	}
}

func TestChatOnce_ToolCalls(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, response: chatToolResp}
	th := newFakeThinker(t, f)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_1" || dec.ToolCalls[0].Name != "calc" || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want call_1/calc/{\"expr\":\"1+1\"}", dec.ToolCalls)
	}
	if dec.Usage != nil {
		t.Errorf("Usage = %+v, want nil (response carries no usage)", dec.Usage)
	}
}

// ---- stream path ----

func TestChatStream_Text(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkText, chunkText2, chunkUsage}}
	gate, sink, got := collectSink()
	th := newFakeThinker(t, f, gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好世界" {
		t.Errorf("Text = %q, want 你好世界", dec.Text)
	}
	if dec.Usage == nil || dec.Usage.Total != 15 {
		t.Errorf("Usage = %+v, want Total=15", dec.Usage)
	}
	if !f.lastStream() {
		t.Error("gate hit should mean a stream request")
	}
	if f.lastTools() != 0 {
		t.Errorf("Tools = %d, want 0 (prompt carries no tools)", f.lastTools())
	}
	if !f.lastIncludeUsage() {
		t.Error("stream request must set stream_options.include_usage")
	}
	for i, want := range []string{"你好", "世界"} {
		if (*got)[i].Kind != ChunkText || (*got)[i].Text != want {
			t.Errorf("chunk %d = %+v, want ChunkText %q", i, (*got)[i], want)
		}
	}
}

func TestChatStream_ToolCalls(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkToolA, chunkToolB}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_1" || dec.ToolCalls[0].Args != `{"expr":"1+1"}` {
		t.Errorf("ToolCalls = %+v, want reassembled call_1", dec.ToolCalls)
	}
	if dec.Text != "" {
		t.Errorf("Text = %q, want empty (pure tool-call round)", dec.Text)
	}
}

// TestChatStream_ToolCallsNoIndex gateways that omit delta.index must not
// lose tool calls: shards accumulate in arrival order, arguments append to
// the latest call.
func TestChatStream_ToolCallsNoIndex(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkToolNoIdxA, chunkToolNoIdxB}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if len(dec.ToolCalls) != 1 || dec.ToolCalls[0].ID != "call_x" || dec.ToolCalls[0].Args != `{"expr":"2+2"}` {
		t.Errorf("ToolCalls = %+v, want arrival-order accumulation", dec.ToolCalls)
	}
}

func TestChatStream_ReasoningFramedSeparately(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkReaso, chunkText}}
	gate, sink, got := collectSink()
	th := newFakeThinker(t, f, gate, sink)

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

func TestChatStream_UsageChunkEmptyChoices(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkText, chunkUsage2}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Text != "你好" {
		t.Errorf("Text = %q, want 你好", dec.Text)
	}
	if dec.Usage == nil || dec.Usage.Total != 3 {
		t.Errorf("Usage = %+v, want Total=3", dec.Usage)
	}
}

// TestChatStream_UsageAllZeroNil an all-zero usage chunk (the server
// shipped the accounting frame without numbers) must yield nil Usage —
// the framework skips the billing event for it.
func TestChatStream_UsageAllZeroNil(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkText, chunkUsageZero}}
	gate, sink, _ := collectSink()
	th := newFakeThinker(t, f, gate, sink)

	dec, err := th.Think(context.Background(), basePrompt())
	if err != nil {
		t.Fatalf("Think: %v", err)
	}
	if dec.Usage != nil {
		t.Errorf("Usage = %+v, want nil (all-zero accounting)", dec.Usage)
	}
}

// ---- dispatch & defenses ----

func TestChatNoTools(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, response: chatEmptyResp}
	th := newFakeThinker(t, f, WithNoTools())

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

// TestThink_SinkNilWithGate gate hit without a sink must not panic —
// chunks are simply discarded (documented contract).
func TestThink_SinkNilWithGate(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, chunks: []string{chunkText}}
	th := newFakeThinker(t, f, WithStreamGate(func() bool { return true }))

	if _, err := th.Think(context.Background(), basePrompt()); err != nil {
		t.Fatalf("Think: %v", err)
	}
}

func TestThink_CtxCanceled(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, response: chatTextResp}
	th := newFakeThinker(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := th.Think(ctx, basePrompt()); err == nil {
		t.Fatal("cancelled ctx must fail fast")
	}
}

func TestNew_Validation(t *testing.T) {
	if _, err := New(Config{Model: "m"}); err == nil {
		t.Error("empty BaseURL must error")
	}
	if _, err := New(Config{BaseURL: "https://api.openai.com/v1"}); err == nil {
		t.Error("empty Model must error")
	}
}

// TestThink_ZeroConfigTimeoutDefaults guards the default resolution: an
// all-zero Config must produce the default transport (120s once timeout,
// stream not body-capped).
func TestThink_ZeroConfigTimeoutDefaults(t *testing.T) {
	f := &fakeServer{t: t, path: fakeChatPath, response: chatTextResp}
	th := newFakeThinker(t, f)
	if th.http.once.Timeout != 120*time.Second {
		t.Errorf("once client Timeout = %v, want 120s default", th.http.once.Timeout)
	}
	if th.http.stream.Timeout != 0 {
		t.Errorf("stream client Timeout = %v, want 0 (streaming must not be body-capped)", th.http.stream.Timeout)
	}
}
