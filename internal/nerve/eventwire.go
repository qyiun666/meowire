// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// eventwire.go — the event stream's portable shape.
//
// A host that journals events (for replay, an audit trail, or another process)
// needs a serialization the framework owns, not whatever `json.Marshal` happens
// to do with an `error` field (it writes `{}` and says nothing was lost). This
// file defines that shape, and the two rules it exists to enforce:
//
//   - a record names its version, and a mismatch is rejected rather than read
//     as a best-effort guess;
//   - anything the wire cannot carry is still carried, but reported: the
//     restored event says in `Dropped` which values arrived as shadows.
//
// Enums travel by name (event kind, loop state, membrane ruling), so a reordered
// iota cannot silently reinterpret a stored stream. Numbers inside payload
// structs (`Signal.Kind`) keep their existing shape — that shape is the
// Session's, not the event's, and changing it is a Session version decision.
package nerve

import (
	"encoding/json"
	"errors"
	"fmt"
)

// eventVersion is the event wire format version. Bump it on any incompatible
// change to WireEvent's shape; DecodeEvent refuses anything else.
const eventVersion = 1

// WireEvent is one Event as it travels. The field set mirrors Event exactly (a
// guard test fails on drift in either direction); three fields change shape on
// purpose — Kind and State become names, and Err becomes a WireErr — while the
// two audit records that used to hold a live value hold its description
// instead (see ReplaceAudit) or degrade its error (see WireVerdict).
type WireEvent struct {
	Version  int           `json:"version"`
	Kind     string        `json:"kind"`
	CellID   string        `json:"cellId,omitempty"`
	Text     string        `json:"text,omitempty"`
	ToolCall *ToolCall     `json:"toolCall,omitempty"`
	Effect   *Effect       `json:"effect,omitempty"`
	State    string        `json:"state,omitempty"`
	Err      *WireErr      `json:"err,omitempty"`
	Output   string        `json:"output,omitempty"`
	Usage    *Usage        `json:"usage,omitempty"`
	Verdict  *WireVerdict  `json:"verdict,omitempty"`
	Wait     *WireWait     `json:"wait,omitempty"`
	Replace  *ReplaceAudit `json:"replace,omitempty"`
	Config   *ConfigAudit  `json:"config,omitempty"`
	Dropped  []string      `json:"dropped,omitempty"`
}

// WireErr is one error as it travels: its text, plus a code when the framework
// owns the value and can restore the identical sentinel.
type WireErr struct {
	Code string `json:"code,omitempty"`
	Text string `json:"text"`
}

// WireVerdict mirrors SandboxVerdict with its error degraded to a WireErr and
// its ruling named.
type WireVerdict struct {
	CellID   string   `json:"cellId,omitempty"`
	Call     ToolCall `json:"call"`
	Ruling   string   `json:"ruling"`
	Reason   string   `json:"reason,omitempty"`
	Question string   `json:"question,omitempty"`
	Err      *WireErr `json:"err,omitempty"`
}

// WireWait mirrors WaitInput with the resume handle in its own serialized form,
// so the Session's version guard stays the thing that rejects a stale
// suspension — an event wire cannot grow laxer than the handle it carries.
type WireWait struct {
	CellID   string          `json:"cellId,omitempty"`
	Call     ToolCall        `json:"call"`
	Question string          `json:"question,omitempty"`
	Session  json.RawMessage `json:"session,omitempty"`
}

// frameworkErrs are the errors the wire can restore by identity, because the
// framework raised them and a consumer may compare them with errors.Is.
var frameworkErrs = []struct {
	code string
	err  error
}{
	{"max-rounds", ErrMaxRounds},
	{"foreign-session", ErrForeignSession},
}

func wireOfErr(err error) *WireErr {
	for _, fe := range frameworkErrs {
		if errors.Is(err, fe.err) {
			return &WireErr{Code: fe.code, Text: err.Error()}
		}
	}
	return &WireErr{Text: err.Error()}
}

// errOf restores an error and reports whether its identity did not survive —
// any error the framework does not own comes back as a fresh value carrying the
// same text, which is a shadow, not the original.
func errOf(w *WireErr) (error, bool) {
	if w == nil {
		return nil, false
	}
	for _, fe := range frameworkErrs {
		if w.Code == fe.code {
			if w.Text == fe.err.Error() {
				return fe.err, false
			}
			// The sentinel arrived wrapped: the chain survives through Unwrap,
			// and the sender's own text survives as this value's Error().
			return wrappedErr{text: w.Text, err: fe.err}, false
		}
	}
	return errors.New(w.Text), true
}

// wrappedErr is a restored error whose framework sentinel stays reachable
// through Unwrap while the printed text remains what the sender wrote.
type wrappedErr struct {
	text string
	err  error
}

func (w wrappedErr) Error() string { return w.text }
func (w wrappedErr) Unwrap() error { return w.err }

// EncodeEvent serializes one event. The only extra failure beyond JSON encoding
// is a Session that cannot be serialized: a suspension handle is not something
// to journal half of.
func EncodeEvent(e Event) ([]byte, error) {
	w := WireEvent{
		Version:  eventVersion,
		Kind:     e.Kind.String(),
		CellID:   e.CellID,
		Text:     e.Text,
		ToolCall: e.ToolCall,
		Effect:   e.Effect,
		Output:   e.Output,
		Usage:    e.Usage,
		Replace:  e.Replace,
		Config:   e.Config,
		Dropped:  e.Dropped,
	}
	if e.Kind == EventState {
		w.State = e.State.String()
	}
	if e.Err != nil {
		w.Err = wireOfErr(e.Err)
	}
	if e.Verdict != nil {
		w.Verdict = &WireVerdict{
			CellID:   e.Verdict.CellID,
			Call:     e.Verdict.Call,
			Ruling:   verdictName(e.Verdict.Ruling),
			Reason:   e.Verdict.Reason,
			Question: e.Verdict.Question,
		}
		if e.Verdict.Err != nil {
			w.Verdict.Err = wireOfErr(e.Verdict.Err)
		}
	}
	if e.Wait != nil {
		raw, err := e.Wait.Session.Marshal()
		if err != nil {
			return nil, fmt.Errorf("nerve: encode event: %w", err)
		}
		w.Wait = &WireWait{
			CellID:   e.Wait.CellID,
			Call:     e.Wait.Call,
			Question: e.Wait.Question,
			Session:  raw,
		}
	}
	out, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("nerve: encode event: %w", err)
	}
	return out, nil
}

// DecodeEvent restores an event written by this build. It fails on malformed
// JSON, a foreign wire version, an unknown kind/state/ruling name, or a
// suspension handle its own version guard rejects. An opaque value is not a
// failure: it arrives as text and the event says so in Dropped.
func DecodeEvent(data []byte) (Event, error) {
	var w WireEvent
	if err := json.Unmarshal(data, &w); err != nil {
		return Event{}, fmt.Errorf("nerve: decode event: %w", err)
	}
	if w.Version != eventVersion {
		return Event{}, fmt.Errorf("nerve: event wire version %d, this build reads %d", w.Version, eventVersion)
	}
	kind, ok := eventKindOf(w.Kind)
	if !ok {
		return Event{}, fmt.Errorf("nerve: decode event: unknown kind %q", w.Kind)
	}
	e := Event{
		Kind:     kind,
		CellID:   w.CellID,
		Text:     w.Text,
		ToolCall: w.ToolCall,
		Effect:   w.Effect,
		Output:   w.Output,
		Usage:    w.Usage,
		Replace:  w.Replace,
		Config:   w.Config,
		Dropped:  w.Dropped,
	}
	if w.State != "" {
		state, ok := loopStateOf(w.State)
		if !ok {
			return Event{}, fmt.Errorf("nerve: decode event: unknown state %q", w.State)
		}
		e.State = state
	}
	if err, lost := errOf(w.Err); err != nil {
		e.Err = err
		if lost {
			e.Dropped = append(e.Dropped, "err.identity")
		}
	}
	verdict, lost, err := decodeVerdict(w.Verdict)
	if err != nil {
		return Event{}, err
	}
	e.Verdict = verdict
	if lost {
		e.Dropped = append(e.Dropped, "verdict.err.identity")
	}
	wait, err := decodeWait(w.Wait)
	if err != nil {
		return Event{}, err
	}
	e.Wait = wait
	return e, nil
}

// decodeVerdict rebuilds one membrane audit record (nil for an absent verdict)
// and reports whether its error arrived as a shadow.
func decodeVerdict(w *WireVerdict) (*SandboxVerdict, bool, error) {
	if w == nil {
		return nil, false, nil
	}
	ruling, ok := verdictOfName(w.Ruling)
	if !ok {
		return nil, false, fmt.Errorf("nerve: decode event: unknown ruling %q", w.Ruling)
	}
	verdict := &SandboxVerdict{
		CellID:   w.CellID,
		Call:     w.Call,
		Ruling:   ruling,
		Reason:   w.Reason,
		Question: w.Question,
	}
	err, lost := errOf(w.Err)
	verdict.Err = err
	return verdict, lost, nil
}

func decodeWait(w *WireWait) (*WaitInput, error) {
	if w == nil {
		return nil, nil
	}
	var sess Session
	if len(w.Session) > 0 {
		var err error
		if sess, err = UnmarshalSession(w.Session); err != nil {
			return nil, fmt.Errorf("nerve: decode event: %w", err)
		}
	}
	return &WaitInput{CellID: w.CellID, Call: w.Call, Question: w.Question, Session: sess}, nil
}

// verdictName is the wire name of a membrane ruling.
func verdictName(v Verdict) string {
	switch v {
	case VerdictAllow:
		return "allow"
	case VerdictAsk:
		return "ask"
	case VerdictDeny:
		return "deny"
	}
	return "unknown"
}

// verdictOfName resolves a wire name; an unknown ruling is rejected rather than
// read as the zero-value Deny.
func verdictOfName(name string) (Verdict, bool) {
	switch name {
	case "deny":
		return VerdictDeny, true
	case "allow":
		return VerdictAllow, true
	case "ask":
		return VerdictAsk, true
	}
	return 0, false
}
