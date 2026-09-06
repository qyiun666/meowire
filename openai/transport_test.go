// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// transport_test.go — HTTP transport units: retry classification, backoff
// (Retry-After honor + cap), SSE line decoding, status error surfacing, and
// an end-to-end retry-then-success round against a flaky fake.

package openai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- retry classification ----

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		name string
		code int
		want bool
	}{
		{"429 rate limited retries", http.StatusTooManyRequests, true},
		{"500 server error retries", http.StatusInternalServerError, true},
		{"502 gateway retries", http.StatusBadGateway, true},
		{"503 overloaded retries", http.StatusServiceUnavailable, true},
		{"200 success does not retry", http.StatusOK, false},
		{"400 bad request does not retry", http.StatusBadRequest, false},
		{"401 unauthorized does not retry", http.StatusUnauthorized, false},
		{"403 forbidden does not retry", http.StatusForbidden, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retryableStatus(c.code); got != c.want {
				t.Errorf("retryableStatus(%d) = %v, want %v", c.code, got, c.want)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	// exponential growth capped at max
	d := backoffDelay(time.Second, 8*time.Second, 0, nil)
	if d < 500*time.Millisecond || d > time.Second {
		t.Errorf("attempt 0 backoff = %v, want ~1s (±half jitter)", d)
	}
	d = backoffDelay(time.Second, 8*time.Second, 10, nil)
	if d < 4*time.Second || d > 8*time.Second {
		t.Errorf("attempt 10 backoff = %v, want capped 8s with half jitter (4s~8s)", d)
	}
	// Retry-After wins when the server asks
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "7")
	if d := backoffDelay(time.Second, 8*time.Second, 0, resp); d != 7*time.Second {
		t.Errorf("Retry-After backoff = %v, want 7s", d)
	}
}

// ---- SSE decoding ----

func TestSSEReader_DataFrames(t *testing.T) {
	r := strings.NewReader("data: {\"a\":1}\n\ndata:{\"b\":2}\n\n: comment\ndata: [DONE]\n\ndata: {\"after\":true}\n")
	sse := newSSEReader(r)
	first, err := sse.next()
	if err != nil || string(first) != `{"a":1}` {
		t.Fatalf("first payload = %q, err %v", first, err)
	}
	second, err := sse.next()
	if err != nil || string(second) != `{"b":2}` {
		t.Fatalf("second payload = %q, err %v (no-space data: prefix must parse)", second, err)
	}
	if _, err := sse.next(); err != io.EOF {
		t.Fatalf("[DONE] must terminate with EOF, got %v", err)
	}
}

func TestSSEReader_CRFLines(t *testing.T) {
	r := strings.NewReader("data: {\"c\":3}\r\n\r\ndata: [DONE]\r\n")
	sse := newSSEReader(r)
	payload, err := sse.next()
	if err != nil || string(payload) != `{"c":3}` {
		t.Fatalf("payload = %q, err %v (CRLF must be tolerated)", payload, err)
	}
	if _, err := sse.next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestSSEReader_MidStreamErrorFrame(t *testing.T) {
	r := strings.NewReader("data: {\"error\":{\"message\":\"boom\",\"code\":1}}\n\n")
	sse := newSSEReader(r)
	if _, err := sse.next(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error frame must fail fast with its message, got %v", err)
	}
}

func TestSSEReader_ErrorFrameAtEOF(t *testing.T) {
	// non-data error payload accumulated until the stream ends
	r := strings.NewReader("{\"error\":{\"message\":\"late boom\"}}\n")
	sse := newSSEReader(r)
	if _, err := sse.next(); err == nil || !strings.Contains(err.Error(), "late boom") {
		t.Fatalf("trailing error payload must surface, got %v", err)
	}
}

func TestSSEReader_PlainEOF(t *testing.T) {
	sse := newSSEReader(strings.NewReader(""))
	if _, err := sse.next(); err != io.EOF {
		t.Fatalf("empty stream must end with EOF, got %v", err)
	}
}

// ---- status error surfacing ----

func TestStatusError_ServerMessage(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"missing field content"}}`)),
		Header:     http.Header{},
	}
	err := statusError(resp)
	if !strings.Contains(err.Error(), "missing field content") {
		t.Errorf("statusError = %v, want the server message", err)
	}
	plain := &http.Response{
		StatusCode: http.StatusTeapot,
		Body:       io.NopCloser(strings.NewReader("plain gateway text")),
		Header:     http.Header{},
	}
	if err := statusError(plain); !strings.Contains(err.Error(), "418") || !strings.Contains(err.Error(), "plain gateway text") {
		t.Errorf("statusError = %v, want status + body snippet", err)
	}
}

// ---- end-to-end retry round ----

// TestRetryThenSuccess a flaky fake (two 503s, then OK) must succeed on the
// third attempt through the same retryClient.
func TestRetryThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatTextResp)
	}))
	t.Cleanup(ts.Close)

	c := newRetryClient(Config{
		BaseURL:    ts.URL,
		Model:      "m",
		BackoffMin: time.Microsecond,
		BackoffMax: 2 * time.Microsecond,
	})
	dec, err := postJSON[chatResponse](context.Background(), c, chatPath, chatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("postJSON: %v", err)
	}
	if len(dec.Choices) == 0 || dec.Choices[0].Message.Content != "你好，世界" {
		t.Errorf("decoded response = %+v, want the third-attempt body", dec)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("attempts = %d, want 3 (two 503 retries then success)", n)
	}
}

// TestRetryExhaustionSurfacesLastBody retry exhaustion against a persistent
// 5xx must surface the last server body (the real error), not a transport
// wrapper.
func TestRetryExhaustionSurfacesLastBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"always down"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)

	c := newRetryClient(Config{
		BaseURL:    ts.URL,
		Model:      "m",
		BackoffMin: time.Microsecond,
		BackoffMax: 2 * time.Microsecond,
	})
	_, err := postJSON[chatResponse](context.Background(), c, chatPath, chatRequest{Model: "m"})
	if err == nil {
		t.Fatal("exhaustion must error")
	}
	if !strings.Contains(err.Error(), "always down") {
		t.Errorf("error = %v, want the last server body message", err)
	}
}

// TestNetworkErrorRetries a connection refused must exhaust retries and
// wrap the transport error.
func TestNetworkErrorRetries(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close() // nothing listens anymore

	c := newRetryClient(Config{
		BaseURL:    url,
		Model:      "m",
		BackoffMin: time.Microsecond,
		BackoffMax: 2 * time.Microsecond,
	})
	_, err := postJSON[chatResponse](context.Background(), c, chatPath, chatRequest{Model: "m"})
	if err == nil {
		t.Fatal("connection refused must error")
	}
	if !strings.Contains(err.Error(), "retries") {
		t.Errorf("error = %v, want a retry-exhaustion wrapper", err)
	}
}

// TestBackoffMinAboveDefaultCap an explicit BackoffMin above the default
// cap must not be silently undercut by the unset BackoffMax default.
func TestBackoffMinAboveDefaultCap(t *testing.T) {
	c := newRetryClient(Config{BaseURL: "http://localhost:1", BackoffMin: 10 * time.Second})
	if c.backoffMax < c.backoffMin {
		t.Errorf("backoffMax = %v < backoffMin = %v, want the default cap floored to the explicit min", c.backoffMax, c.backoffMin)
	}
}
