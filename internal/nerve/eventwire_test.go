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
	eff := &Effect{Result: "42 hits", Err: "denied", WaitInput: "confirm?"}
	sess := Session{cell: "c1", round: 2, input: "go", plan: "p", context: []string{"a"}, output: "o",
		pending: *call, remaining: []ToolCall{{ID: "t2"}}, toolResults: []ToolResult{{ID: "t1", Result: "r"}},
		kind: waitUtterance, utterance: "draft"}
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
		// A restored event carries its own loss report onward.
		{Kind: EventText, CellID: "c1", Text: "restored", Dropped: []string{"err.identity"}},
	}
}

// TestEventWireRoundTrip: every kind survives encode → decode unchanged,
// including the loss report a restored event carries onward.
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
// error and a suspension handle; neither may arrive as a shadow, and no event
// gains or loses a name in its loss report on the way through.
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
		if !slices.Equal(got.Dropped, ev.Dropped) {
			t.Errorf("%v reported dropped fields %v, want %v", ev.Kind, got.Dropped, ev.Dropped)
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

// TestEnumNamesCoverEveryValue: every encoded enum travels by a table name, so a
// value added without a name would be refused at write time. This catches it at
// build time instead.
func TestEnumNamesCoverEveryValue(t *testing.T) {
	look := map[string]func(string) bool{
		"loop state": func(n string) bool { _, ok := loopStateOf(n); return ok },
		"ruling":     func(n string) bool { _, ok := verdictOfName(n); return ok },
		"wait kind":  func(n string) bool { _, ok := waitKindOf(n); return ok },
	}
	for _, tb := range []struct {
		what  string
		names []string
		n     int
	}{
		{"loop state", loopStateNames, int(StateError) + 1},
		{"ruling", verdictNames, int(VerdictAsk) + 1},
		{"wait kind", waitKindNames, int(waitUtterance) + 1},
	} {
		if len(tb.names) != tb.n {
			t.Errorf("%d %s names for %d values", len(tb.names), tb.what, tb.n)
		}
		for i, name := range tb.names {
			if name == "" || name == "unknown" {
				t.Errorf("%s %d has no usable wire name (%q)", tb.what, i, name)
				continue
			}
			if !look[tb.what](name) {
				t.Errorf("%s %q does not resolve back", tb.what, name)
			}
		}
	}
}

// TestWireFixtureCarriesEveryField: the round-trip guards can only prove what
// they exercise. A field that no fixture event sets would survive a wire that
// never carries it, so every field of Event must arrive non-zero somewhere in
// the fixture set.
func TestWireFixtureCarriesEveryField(t *testing.T) {
	typ := reflect.TypeFor[Event]()
	events := wireEvents()
	cover := map[string]bool{}
	for _, e := range events {
		v := reflect.ValueOf(e)
		for i := range v.NumField() {
			if !v.Field(i).IsZero() {
				cover[typ.Field(i).Name] = true
			}
		}
	}
	for _, name := range fieldNames(typ) {
		if !cover[name] {
			t.Errorf("no fixture event sets Event.%s, so the round-trip guards cannot see it", name)
		}
	}
}

// TestWireCarriesNoBareError: marshaling a struct that holds an error writes it
// as an empty object and reports nothing lost — the hole this whole file exists
// to close. Every error on the wire must therefore arrive as a WireErr, and a
// payload struct that grows a bare error field breaks here.
func TestWireCarriesNoBareError(t *testing.T) {
	errType := reflect.TypeFor[error]()
	var walk func(typ reflect.Type, path string, seen map[reflect.Type]bool) []string
	walk = func(typ reflect.Type, path string, seen map[reflect.Type]bool) []string {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			return walk(typ.Elem(), path, seen)
		case reflect.Struct:
			if seen[typ] {
				return nil
			}
			seen[typ] = true
			var found []string
			for i := range typ.NumField() {
				f := typ.Field(i)
				next := path + "." + f.Name
				if f.Type == errType {
					found = append(found, next)
					continue
				}
				found = append(found, walk(f.Type, next, seen)...)
			}
			return found
		}
		return nil
	}
	if found := walk(reflect.TypeFor[WireEvent](), "WireEvent", map[reflect.Type]bool{}); found != nil {
		t.Fatalf("the wire carries bare error fields, which JSON cannot write: %v", found)
	}
}

// TestStateNameOnlyOnStateRecord: a state name belongs to one kind. Missing on a
// state record it would decode as an invented StateIdle; present on any other
// record it is something this build never writes.
func TestStateNameOnlyOnStateRecord(t *testing.T) {
	noState := tamperedRecord(mustEncode(t, Event{Kind: EventState, State: StateActing}), map[string]any{"state": nil})
	if _, err := DecodeEvent(noState); err == nil {
		t.Fatal("a state record with no state name decoded as some state")
	}
	stray := mustEncode(t, Event{Kind: EventText, Text: "x"})
	stray = tamperedRecord(stray, map[string]any{"state": "acting"})
	if _, err := DecodeEvent(stray); err == nil {
		t.Fatal("a state name on a text record decoded")
	}
}

// TestUnnamedRulingRefusedAtEncode: a ruling this build has no name for cannot
// be read back, so it is refused on the way out instead of being written as a
// record no decoder accepts.
func TestUnnamedRulingRefusedAtEncode(t *testing.T) {
	if _, err := EncodeEvent(Event{Kind: EventSandbox, Verdict: &SandboxVerdict{Ruling: Verdict(42)}}); err == nil {
		t.Fatal("an unnamed ruling was encoded")
	}
	// The mirror of DecodeEvent's rule: a state value on another kind is not a
	// record this build can journal, and dropping it silently would be a loss no
	// one reported.
	if _, err := EncodeEvent(Event{Kind: EventText, Text: "x", State: StateWaiting}); err == nil {
		t.Fatal("a state value on a text event was encoded by dropping it")
	}
	// A value past the end of a name table used to encode as "unknown" — a record
	// whose own name guarantees DecodeEvent will refuse it. Better no entry than a
	// poison entry.
	if _, err := EncodeEvent(Event{Kind: EventState, State: LoopState(42)}); err == nil {
		t.Fatal("an unnamed loop state was encoded")
	}
	if _, err := EncodeEvent(Event{Kind: EventKind(42)}); err == nil {
		t.Fatal("an unnamed event kind was encoded")
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
