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
	"slices"
)

// eventVersion is the event wire format version. Bump it on any incompatible
// change to WireEvent's shape; DecodeEvent refuses anything else.
const eventVersion = 1

// WireEvent is one Event as it travels. The field set mirrors Event exactly (a
// guard test fails on drift in either direction); three fields change shape on
// purpose — Kind and State become names, and Err becomes a WireErr — while the
// two audit records carry a description of a port (see ReplaceAudit) or a
// degraded error (see WireVerdict) instead of a live value.
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

// EncodeEvent serializes one event. Beyond JSON encoding it fails on two values
// it cannot faithfully hand back: a Session that cannot be serialized (a
// suspension handle is not something to journal half of) and a ruling this
// build has no name for (the record could never be decoded again).
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
	} else if e.State != 0 {
		return nil, fmt.Errorf("nerve: encode event: state %q on kind %q", e.State, e.Kind)
	}
	if e.Err != nil {
		w.Err = wireOfErr(e.Err)
	}
	verdict, err := encodeVerdict(e.Verdict)
	if err != nil {
		return nil, err
	}
	w.Verdict = verdict
	wait, err := encodeWait(e.Wait)
	if err != nil {
		return nil, err
	}
	w.Wait = wait
	out, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("nerve: encode event: %w", err)
	}
	return out, nil
}

// encodeVerdict mirrors one membrane audit record, refusing a ruling this build
// has no name for: the record could never be decoded again.
func encodeVerdict(v *SandboxVerdict) (*WireVerdict, error) {
	if v == nil {
		return nil, nil
	}
	ruling := verdictName(v.Ruling)
	if ruling == "" {
		return nil, fmt.Errorf("nerve: encode event: unknown ruling %d", v.Ruling)
	}
	w := &WireVerdict{
		CellID:   v.CellID,
		Call:     v.Call,
		Ruling:   ruling,
		Reason:   v.Reason,
		Question: v.Question,
	}
	if v.Err != nil {
		w.Err = wireOfErr(v.Err)
	}
	return w, nil
}

// encodeWait mirrors one suspension, carrying the handle as its own serialized
// bytes so its version guard stays the thing that rejects a stale one.
func encodeWait(w *WaitInput) (*WireWait, error) {
	if w == nil {
		return nil, nil
	}
	raw, err := w.Session.Marshal()
	if err != nil {
		return nil, fmt.Errorf("nerve: encode event: %w", err)
	}
	return &WireWait{CellID: w.CellID, Call: w.Call, Question: w.Question, Session: raw}, nil
}

// DecodeEvent restores an event written by this build. It fails on malformed
// JSON, a foreign wire version, an unknown kind/state/ruling name (a state
// record with no state name included), a state name on any other kind, or a
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
	state, err := decodeState(kind, w.State)
	if err != nil {
		return Event{}, err
	}
	e.State = state
	if restored, lost := errOf(w.Err); restored != nil {
		e.Err = restored
		if lost {
			e.Dropped = reportLoss(e.Dropped, "err.identity")
		}
	}
	verdict, lost, err := decodeVerdict(w.Verdict)
	if err != nil {
		return Event{}, err
	}
	e.Verdict = verdict
	if lost {
		e.Dropped = reportLoss(e.Dropped, "verdict.err.identity")
	}
	e.Wait, err = decodeWait(w.Wait)
	if err != nil {
		return Event{}, err
	}
	return e, nil
}

// reportLoss names a value that arrived as a shadow. A record that has already
// been through the wire once says so once: re-journaling a restored event must
// not pile the same name up.
func reportLoss(dropped []string, what string) []string {
	if slices.Contains(dropped, what) {
		return dropped
	}
	return append(dropped, what)
}

// decodeState resolves the name a state record carries. The name belongs to that
// kind alone: a state record without one would decode as an invented StateIdle,
// and a name on any other kind is not something this build writes.
func decodeState(kind EventKind, name string) (LoopState, error) {
	if kind != EventState {
		if name != "" {
			return 0, fmt.Errorf("nerve: decode event: state %q on kind %q", name, kind)
		}
		return 0, nil
	}
	state, ok := loopStateOf(name)
	if !ok {
		return 0, fmt.Errorf("nerve: decode event: unknown state %q", name)
	}
	return state, nil
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
