// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// eventwire_test.go — the event wire: round trip, rejection, and drop reporting.
package nerve

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// wireEvents returns one non-zero Event per kind — the set every round-trip
// assertion runs over, so a new kind is only covered once it appears here too.
func wireEvents() []Event {
	call := &ToolCall{ID: "t1", Name: "search", Args: `{"q":"x"}`}
	eff := &Effect{Result: "42 hits", Err: "denied", WaitInput: "confirm?", Send: &Signal{ID: "c1/1", To: "peer", Kind: KindResponse, Status: TaskCompleted, Payload: []byte("p")}}
	sess := Session{cell: "c1", round: 2, input: "go", plan: "p", context: []string{"a"}, output: "o",
		pending: *call, remaining: []ToolCall{{ID: "t2"}}, toolResults: []ToolResult{{ID: "t1", Result: "r"}},
		requests: []Signal{{ID: "c1/0", Kind: KindStimulus}}, kind: waitUtterance, utterance: "draft"}
	return []Event{
		{Kind: EventText, CellID: "c1", Text: "thinking"},
		{Kind: EventToolCall, CellID: "c1", ToolCall: call},
		{Kind: EventToolResult, CellID: "c1", ToolCall: call, Effect: eff},
		{Kind: EventState, CellID: "c1", State: StateWaiting},
		{Kind: EventDone, CellID: "c1", Output: "the answer"},
		{Kind: EventError, CellID: "c1", Err: ErrMaxRounds},
		{Kind: EventUsage, CellID: "c1", Usage: &Usage{Prompt: 10, Completion: 5, Total: 15}},
		{Kind: EventSandbox, CellID: "c1", Verdict: &SandboxVerdict{CellID: "c1", Call: *call, Ruling: VerdictAsk, Reason: "r", Question: "sure?"}},
		{Kind: EventWaitInput, CellID: "c1", Wait: &WaitInput{CellID: "c1", Call: *call, Question: "?", Session: sess}},
		{Kind: EventPaused, CellID: "c1", Wait: &WaitInput{CellID: "c1", Session: sess}},
		{Kind: EventReplace, CellID: "c1", Replace: &ReplaceAudit{CellID: "c1", Slot: "think", OldType: "nerve.a", NewType: "nerve.b"}},
		{Kind: EventConfig, CellID: "c1", Config: &ConfigAudit{CellID: "c1", Old: LoopConfig{MaxRounds: 2}, New: LoopConfig{MaxRounds: 9, ParallelActs: true, MaxParallelActs: 3}}},
	}
}

// TestEventWireRoundTrip: every kind survives encode → decode unchanged, and
// nothing reports itself as dropped.
func TestEventWireRoundTrip(t *testing.T) {
	for _, want := range wireEvents() {
		data, err := EncodeEvent(want)
		if err != nil {
			t.Fatalf("encode %v: %v", want.Kind, err)
		}
		got, err := DecodeEvent(data)
		if err != nil {
			t.Fatalf("decode %v: %v", want.Kind, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%v round trip = %+v, want %+v", want.Kind, got, want)
		}
	}
}

// TestEventWireDropsNothingForFrameworkValues: the round trip above carries an
// error and a suspension handle; neither may arrive as a shadow.
func TestEventWireDropsNothingForFrameworkValues(t *testing.T) {
	for _, ev := range wireEvents() {
		data, err := EncodeEvent(ev)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := DecodeEvent(data)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Dropped) != 0 {
			t.Errorf("%v reported dropped fields %v, want none", ev.Kind, got.Dropped)
		}
	}
}

// TestEventVersionMismatchRejected: a record from another wire version is
// rejected, never read as a best-effort match.
func TestEventVersionMismatchRejected(t *testing.T) {
	data, err := EncodeEvent(Event{Kind: EventText, Text: "x"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, version := range []int{0, 2} {
		tampered := tamperedRecord(data, map[string]any{"version": float64(version)})
		if _, err := DecodeEvent(tampered); err == nil {
			t.Errorf("version %d decoded without complaint", version)
		}
	}
	// The current version must decode, or the guard above proves nothing.
	if _, err := DecodeEvent(data); err != nil {
		t.Fatalf("the supported version was rejected: %v", err)
	}
}

// TestUnknownKindRejected: an event whose kind name this build does not know is
// refused rather than defaulted.
func TestUnknownKindRejected(t *testing.T) {
	tampered := tamperedRecord(mustEncode(t, Event{Kind: EventText, Text: "x"}), map[string]any{"kind": "quantum-leap"})
	if _, err := DecodeEvent(tampered); err == nil {
		t.Fatal("an unknown kind was decoded")
	}
	// A missing state name on a state event is the same class of lie.
	tampered = tamperedRecord(mustEncode(t, Event{Kind: EventState, State: StateActing}), map[string]any{"state": "sleepwalking"})
	if _, err := DecodeEvent(tampered); err == nil {
		t.Fatal("an unknown state was decoded")
	}
	tampered = tamperedRecord(mustEncode(t, Event{Kind: EventSandbox, Verdict: &SandboxVerdict{Ruling: VerdictAllow}}),
		map[string]any{"verdict": map[string]any{"ruling": "maybe"}})
	if _, err := DecodeEvent(tampered); err == nil {
		t.Fatal("an unknown ruling was decoded")
	}
}

// TestSentinelErrorSurvivesWire: a framework error comes back as the identical
// value, so a host's errors.Is keeps working across a journal.
func TestSentinelErrorSurvivesWire(t *testing.T) {
	for _, sentinel := range []error{ErrMaxRounds, ErrForeignSession} {
		wrapped := fmt.Errorf("loop ended: %w", sentinel)
		got, err := DecodeEvent(mustEncode(t, Event{Kind: EventError, Err: wrapped}))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !errors.Is(got.Err, sentinel) {
			t.Fatalf("restored error = %v, want it still to match %v", got.Err, sentinel)
		}
		if got.Err.Error() != wrapped.Error() {
			t.Errorf("restored text = %q, want %q", got.Err.Error(), wrapped.Error())
		}
		if len(got.Dropped) != 0 {
			t.Errorf("Dropped = %v, want none for a framework error", got.Dropped)
		}
	}
}

// TestOpaqueFieldsReportDropped: a host error has no framework identity to
// restore, so the text is all that survives — and the event says so instead of
// letting a comparison quietly return false.
func TestOpaqueFieldsReportDropped(t *testing.T) {
	hostErr := errors.New("rate limited")
	got, err := DecodeEvent(mustEncode(t, Event{Kind: EventError, Err: hostErr}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Err == hostErr || errors.Is(got.Err, hostErr) {
		t.Fatal("a host error cannot come back as the same value")
	}
	if got.Err.Error() != "rate limited" {
		t.Errorf("text = %q, want it preserved", got.Err.Error())
	}
	if !slices.Contains(got.Dropped, "err.identity") {
		t.Fatalf("Dropped = %v, want err.identity named", got.Dropped)
	}

	vd, err := DecodeEvent(mustEncode(t, Event{Kind: EventSandbox, Verdict: &SandboxVerdict{Ruling: VerdictDeny, Err: hostErr}}))
	if err != nil {
		t.Fatalf("decode verdict: %v", err)
	}
	if vd.Verdict.Err == nil || vd.Verdict.Err.Error() != "rate limited" {
		t.Fatalf("verdict error = %v, want the text preserved (json.Marshal of an error field writes {})", vd.Verdict.Err)
	}
	if !slices.Contains(vd.Dropped, "verdict.err.identity") {
		t.Fatalf("Dropped = %v, want verdict.err.identity named", vd.Dropped)
	}
}

// TestBareJSONErrorFieldWouldLoseText: the negative that justifies the mirror —
// marshaling Event directly writes an error field as an empty object.
func TestBareJSONErrorFieldWouldLoseText(t *testing.T) {
	raw, err := json.Marshal(Event{Kind: EventError, Err: errors.New("rate limited")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"Err":{}`) {
		t.Fatalf("plain JSON = %s, want an error field collapsed to {}", raw)
	}
}

// TestSessionInsideEventStillGuardsVersion: the event wire cannot grow laxer
// than the suspension handle it carries.
func TestSessionInsideEventStillGuardsVersion(t *testing.T) {
	tampered := tamperedRecord(mustEncode(t, Event{Kind: EventWaitInput, Wait: &WaitInput{
		CellID: "c1", Session: Session{cell: "c1", round: 1, kind: waitTool, pending: ToolCall{ID: "t1"}},
	}}), map[string]any{"wait": map[string]any{"session": map[string]any{"version": float64(99)}}})
	if _, err := DecodeEvent(tampered); err == nil {
		t.Fatal("a foreign Session version inside an event was accepted")
	}
}

// TestEventWireCoversEveryEventField: WireEvent mirrors Event. A field added to
// one side and not the other is either silently lost or silently invented.
func TestEventWireCoversEveryEventField(t *testing.T) {
	wire := fieldNames(reflect.TypeFor[WireEvent]())
	liveNames := fieldNames(reflect.TypeFor[Event]())
	for _, name := range liveNames {
		if !slices.Contains(wire, name) {
			t.Errorf("Event.%s has no counterpart on WireEvent (it would be dropped silently)", name)
		}
	}
	for _, name := range wire {
		if name == "Version" {
			continue // the wire's own envelope field
		}
		if !slices.Contains(liveNames, name) {
			t.Errorf("WireEvent.%s has no counterpart on Event", name)
		}
	}
}

// TestEventKindNamesCoverEveryKind: the name table is indexed by kind value, so
// a kind added to the iota without a name would decode as "unknown" and be
// rejected — this catches it at build time instead.
func TestEventKindNamesCoverEveryKind(t *testing.T) {
	if len(eventKindNames) != int(EventConfig)+1 {
		t.Fatalf("%d kind names for %d kinds", len(eventKindNames), int(EventConfig)+1)
	}
	for k := EventText; k <= EventConfig; k++ {
		if k.String() == "unknown" {
			t.Errorf("kind %d has no wire name", k)
		}
		if _, ok := eventKindOf(k.String()); !ok {
			t.Errorf("kind %q does not resolve back", k)
		}
	}
}

func mustEncode(t *testing.T, e Event) []byte {
	t.Helper()
	data, err := EncodeEvent(e)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return data
}

// tamperedRecord rewrites top-level fields of an encoded event, standing in for a
// record written by another (or older) build.
func tamperedRecord(data []byte, overrides map[string]any) []byte {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		panic(err)
	}
	for k, v := range overrides {
		doc[k] = v
	}
	out, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return out
}

func fieldNames(typ reflect.Type) []string {
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name)
	}
	return names
}
