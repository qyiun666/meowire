// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// transport.go — HTTP transport with retry and SSE line decoding, stdlib
// only (no go-openai, no retryablehttp). Retry policy: 120s timeout, 3
// retries with 1s~8s exponential backoff, only transient failures retried
// (network errors, 429, 5xx — a 4xx business error can never succeed on
// retry, so it fails fast).

package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout    = 120 * time.Second
	defaultRetryMax   = 3
	defaultBackoffMin = time.Second
	defaultBackoffMax = 8 * time.Second
	maxIdleConns      = 10
	maxIdlePerHost    = 5
	maxErrAccumulator = 1 << 20 // error-frame accumulator cap (broken-server guard)
)

// retryClient posts JSON payloads with retry on transient failures and
// decodes SSE streams. Once and streaming share one Transport but carry
// different timeouts: a whole-body client timeout would silently cap every
// stream at that timeout (http.Client.Timeout includes body reads), so the
// streaming client only bounds time-to-headers via ResponseHeaderTimeout —
// a live SSE body then runs until ctx cancellation or server close.
type retryClient struct {
	once       *http.Client
	stream     *http.Client
	base       string
	apiKey     string
	retryMax   int
	backoffMin time.Duration
	backoffMax time.Duration
}

func newRetryClient(cfg Config) *retryClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	retryMax := defaultRetryMax
	if cfg.RetryMax > 0 {
		retryMax = cfg.RetryMax
	} else if cfg.RetryMax < 0 {
		retryMax = 0 // explicit opt-out
	}
	bmin := cfg.BackoffMin
	if bmin <= 0 {
		bmin = defaultBackoffMin
	}
	bmax := cfg.BackoffMax
	if bmax <= 0 {
		bmax = defaultBackoffMax
		if bmax < bmin { // BackoffMax unset: don't let the default cap undercut an explicit min
			bmax = bmin
		}
	}
	transport := &http.Transport{
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdlePerHost,
		ResponseHeaderTimeout: timeout,
	}
	return &retryClient{
		once:       &http.Client{Transport: transport, Timeout: timeout},
		stream:     &http.Client{Transport: transport},
		base:       strings.TrimSuffix(cfg.BaseURL, "/"),
		apiKey:     cfg.APIKey,
		retryMax:   retryMax,
		backoffMin: bmin,
		backoffMax: bmax,
	}
}

// post sends one JSON request, retrying transient failures (network errors,
// 429, 5xx — honoring Retry-After when the server asks). A non-retryable
// status returns as-is so the caller surfaces the error body; on retry
// exhaustion a surviving response returns the same way (the last server
// answer carries the real error), while pure network exhaustion wraps the
// last transport error.
func (c *retryClient) post(ctx context.Context, path string, payload any, stream bool) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	url := c.base + path
	var (
		lastResp *http.Response
		lastErr  error
	)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("openai: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		hc := c.once
		if stream {
			hc = c.stream
		}
		resp, err := hc.Do(req)
		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil
		}
		if lastResp != nil {
			drainClose(lastResp)
		}
		lastResp, lastErr = resp, err
		if attempt >= c.retryMax {
			if lastResp != nil {
				return lastResp, nil
			}
			return nil, fmt.Errorf("openai: request failed after %d retries: %w", c.retryMax, lastErr)
		}
		if err := sleepBackoff(ctx, c.backoffMin, c.backoffMax, attempt, lastResp); err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
	}
}

// retryableStatus reports whether an HTTP status deserves another attempt.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// sleepBackoff waits before the next retry attempt: Retry-After when the
// server asked, otherwise exponential growth capped at backoffMax with a
// half-jitter. A cancelled ctx aborts the wait.
func sleepBackoff(ctx context.Context, min, max time.Duration, attempt int, resp *http.Response) error {
	d := backoffDelay(min, max, attempt, resp)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func backoffDelay(min, max time.Duration, attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			if secs, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && secs > 0 {
				return time.Duration(secs) * time.Second
			}
		}
	}
	d := min << uint(attempt)
	if d <= 0 || d > max {
		d = max // overflow or cap
	}
	return d/2 + time.Duration(rand.Int63n(int64(d)/2+1))
}

func drainClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8192)) // best-effort keep-alive drain
	resp.Body.Close()
}

// postJSON posts a JSON payload and decodes a JSON response (the
// single-shot path shared by both wires).
func postJSON[T any](ctx context.Context, c *retryClient, path string, payload any) (*T, error) {
	resp, err := c.post(ctx, path, payload, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	var out T
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	return &out, nil
}

// statusError surfaces a non-200 API error with the server's message when
// the body carries the OpenAI error shape, else with a body snippet.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	var e struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != nil && e.Error.Message != "" {
		return fmt.Errorf("openai: llm status %d: %s", resp.StatusCode, e.Error.Message)
	}
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Errorf("openai: llm status %d", resp.StatusCode)
	}
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	return fmt.Errorf("openai: llm status %d: %s", resp.StatusCode, snippet)
}

// sseReader decodes an SSE byte stream into data payloads: blank and
// non-data lines are skipped (frames accumulate for the terminal error),
// "data: [DONE]" terminates the stream, everything else yields its JSON
// payload. A `data: {"error": ...}` frame fails fast.
type sseReader struct {
	sc     *bufio.Scanner
	errAcc bytes.Buffer
}

func newSSEReader(r io.Reader) *sseReader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // 1MB max line: large tool-call argument deltas
	return &sseReader{sc: sc}
}

func (s *sseReader) next() ([]byte, error) {
	for s.sc.Scan() {
		line := bytes.TrimSpace(s.sc.Bytes())
		if len(line) == 0 {
			continue
		}
		payload, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			s.errAcc.Write(line)
			s.errAcc.WriteByte('\n')
			if s.errAcc.Len() > maxErrAccumulator {
				return nil, errors.New("openai: stream sent too many non-data frames")
			}
			continue
		}
		payload = bytes.TrimSpace(payload)
		if string(payload) == "[DONE]" {
			return nil, io.EOF
		}
		if msg, ok := streamError(payload); ok {
			return nil, errors.New("openai: stream error: " + msg)
		}
		return payload, nil
	}
	if err := s.sc.Err(); err != nil {
		return nil, fmt.Errorf("openai: stream read: %w", err)
	}
	if msg, ok := streamError(s.errAcc.Bytes()); ok {
		return nil, errors.New("openai: stream error: " + msg)
	}
	return nil, io.EOF
}

// streamError extracts the message from an OpenAI error payload
// (`{"error":{"message":...}}`), both mid-stream and accumulated at EOF.
func streamError(payload []byte) (string, bool) {
	var e struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &e) != nil || e.Error == nil || e.Error.Message == "" {
		return "", false
	}
	return e.Error.Message, true
}
