// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// lifecycle_test.go — boot phase, boot rollback, and blueprint coverage tests.
package meowire

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// lifecycleLog records the order in which the framework touches organs.
type lifecycleLog struct {
	boots     []string
	closes    []string
	failBoot  string
	failClose error
}

// errUnavailable is what an instrumented organ returns when the test asks it to
// fail — a sentinel, so assertions can check the framework wrapped it rather
// than flattened it into its own message.
var errUnavailable = errors.New("organ unavailable")

func (l *lifecycleLog) boot(name string) error {
	l.boots = append(l.boots, name)
	if l.failBoot == name {
		return errUnavailable
	}
	return nil
}

func (l *lifecycleLog) close(name string) error {
	l.closes = append(l.closes, name)
	return l.failClose
}

// Bootable probes: one per port whose carrier type the api test package may
// define (Hooks and Budget are concrete structs, so they stay uninstrumented —
// booting them is not something a host can declare either).
type probeEffector struct{ log *lifecycleLog }

func (p probeEffector) Act(context.Context, Action) (*Effect, error) {
	return &Effect{Result: "ok"}, nil
}
func (p probeEffector) Boot(context.Context) error { return p.log.boot("Act") }

type probeSandbox struct{ log *lifecycleLog }

func (p probeSandbox) Allow(context.Context, Action) (Verdict, string, error) {
	return VerdictAllow, "", nil
}
func (p probeSandbox) Emit(context.Context, Utterance) (Verdict, string, error) {
	return VerdictAllow, "", nil
}
func (p probeSandbox) Bounds() string             { return "test bounds" }
func (p probeSandbox) Boot(context.Context) error { return p.log.boot("Sandbox") }

type probeMemory struct{ log *lifecycleLog }

func (p probeMemory) Recall(context.Context, MemoryQuery) ([]Record, error) {
	return nil, nil
}
func (p probeMemory) Remember(context.Context, CycleFacts) error { return nil }
func (p probeMemory) Boot(context.Context) error                 { return p.log.boot("Mem") }

type probeCloser struct{ log *lifecycleLog }

func (p *probeCloser) Boot(context.Context) error { return p.log.boot("Closer") }
func (p *probeCloser) Close() error               { return p.log.close("Closer") }

// bootableOrgans returns a complete assembly whose four instrumentable host
// ports all declare the lifecycle capability.
func bootableOrgans() (Organs, *lifecycleLog) {
	log := &lifecycleLog{}
	o := fullOrgans()
	o.Act = probeEffector{log}
	o.Sandbox = probeSandbox{log}
	o.Mem = probeMemory{log}
	o.Closer = &probeCloser{log}
	return o, log
}

// TestBootOrderMatchesBlueprint: hostOrgans and the blueprint are two lists of
// the same six host ports; this is the guard that makes them one fact. Without it
// a port added to the blueprint would simply never be booted, silently.
func TestBootOrderMatchesBlueprint(t *testing.T) {
	var got []string
	for _, ref := range hostOrgans(fullOrgans()) {
		got = append(got, ref.name)
	}
	if want := PortOrder(); !slices.Equal(got, want) {
		t.Fatalf("hostOrgans order = %v, want the blueprint's required phase-2 ports %v", got, want)
	}
}

// TestBootsInPortOrder: organs come up in blueprint order, each exactly once,
// and only the organs that declared Boot are touched.
func TestBootsInPortOrder(t *testing.T) {
	o, log := bootableOrgans()
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	// The expectation is derived from the blueprint, not hardcoded: the
	// instrumented ports in PortOrder sequence.
	instrumented := map[string]bool{"Act": true, "Closer": true, "Sandbox": true, "Mem": true}
	var want []string
	for _, name := range PortOrder() {
		if instrumented[name] {
			want = append(want, name)
		}
	}
	if !slices.Equal(log.boots, want) {
		t.Fatalf("boot order = %v, want %v", log.boots, want)
	}
}

// TestUninstrumentedAssemblyBootsNothing: Boot is derived from the port value,
// never required — a host that declares no lifecycle gets no lifecycle calls.
func TestUninstrumentedAssemblyBootsNothing(t *testing.T) {
	a, err := New(Blueprint{Organs: fullOrgans(), Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()
}

// TestBootFailureAbortsAssemblyAndReleasesThroughCloser: the first failing Boot
// stops the sequence (later ports never open) and what the attempt touched is
// released through the host's Closer — the framework has no other cleanup
// channel and never closes an organ itself.
func TestBootFailureAbortsAssemblyAndReleasesThroughCloser(t *testing.T) {
	o, log := bootableOrgans()
	log.failBoot = "Sandbox"

	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if a != nil {
		t.Fatal("a failed boot still returned an agent")
	}
	if err == nil {
		t.Fatal("a failed boot was reported as success")
	}
	if !strings.Contains(err.Error(), "boot Sandbox") {
		t.Errorf("error = %q, want the failing port named", err)
	}
	if !errors.Is(err, errUnavailable) {
		t.Errorf("error = %q, want the organ's own error wrapped, not flattened", err)
	}
	if slices.Contains(log.boots, "Mem") {
		t.Errorf("boot continued past the failure: %v", log.boots)
	}
	if !slices.Equal(log.closes, []string{"Closer"}) {
		t.Errorf("cleanup = %v, want exactly one Closer call", log.closes)
	}
}

// TestBootCleanupFailureIsReported: when the release itself fails, the assembly
// error says so instead of folding a left-open resource into the boot error.
func TestBootCleanupFailureIsReported(t *testing.T) {
	o, log := bootableOrgans()
	log.failBoot = "Act"
	log.failClose = errors.New("close refused")

	_, err := New(Blueprint{Organs: o, Config: Config{}})
	if err == nil {
		t.Fatal("a failed cleanup was reported as success")
	}
	for _, want := range []string{"boot Act", "cleanup after a failed boot", "close refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

// TestCloserBootSlotFollowsTheHost: P3b is filled exactly when the host's
// Closer can boot — the blueprint reports the optional lifecycle point as the
// host declared it, not as the framework assumed it.
func TestCloserBootSlotFollowsTheHost(t *testing.T) {
	o, _ := bootableOrgans()
	if !slotFilled(o, "P3b") {
		t.Error("P3b should be filled when the Closer implements Bootable")
	}
	plain := fullOrgans()
	if slotFilled(plain, "P3b") {
		t.Error("P3b should be unwired when the Closer cannot boot")
	}
}

// TestReplaceBootsBeforeSwapping: a replacement organ is brought up first, so a
// dead organ never takes effect mid-round; the old wiring stays in place.
func TestReplaceBootsBeforeSwapping(t *testing.T) {
	o, _ := bootableOrgans()
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	nextLog := &lifecycleLog{}
	next := probeEffector{nextLog}
	old, err := a.Replace(SlotAct, next)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if old == nil {
		t.Error("Replace must return the previous port")
	}
	if !slices.Equal(nextLog.boots, []string{"Act"}) {
		t.Fatalf("replacement boot calls = %v, want exactly one", nextLog.boots)
	}
	// The swap landed: replacing it again returns the organ we just installed.
	third := probeEffector{&lifecycleLog{}}
	if back, err := a.Replace(SlotAct, third); err != nil || back != any(next) {
		t.Fatalf("second Replace returned (%v, %v), want the first replacement back", back, err)
	}
}

// TestReplaceBootFailureLeavesWiringAlone: the failed replacement is refused
// before it is committed, so the live port is still the one it replaced.
func TestReplaceBootFailureLeavesWiringAlone(t *testing.T) {
	o, _ := bootableOrgans()
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	healthy := probeEffector{&lifecycleLog{}}
	if _, err := a.Replace(SlotAct, healthy); err != nil {
		t.Fatalf("replace: %v", err)
	}
	brokenLog := &lifecycleLog{failBoot: "Act"}
	if _, err := a.Replace(SlotAct, probeEffector{brokenLog}); err == nil {
		t.Fatal("a replacement that failed to boot was accepted")
	} else if !strings.Contains(err.Error(), "replace act") {
		t.Errorf("error = %q, want the refused slot named", err)
	}
	// A successful Replace now must hand back the healthy organ, proving the
	// broken one was never installed.
	if back, err := a.Replace(SlotAct, probeEffector{&lifecycleLog{}}); err != nil || back != any(healthy) {
		t.Fatalf("after a refused swap, Replace returned (%v, %v), want the healthy organ back", back, err)
	}
}

// TestReplaceRejectsBadSlotWithoutBooting: an organ gets one startup per
// assembly, so a mistyped slot name is refused before Boot — otherwise the
// organ spends its lifecycle on a swap that never happened, and arrives at the
// slot it does belong to already used.
func TestReplaceRejectsBadSlotWithoutBooting(t *testing.T) {
	o, _ := bootableOrgans()
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()

	log := &lifecycleLog{}
	if _, err := a.Replace("acting", probeEffector{log}); err == nil {
		t.Fatal("a slot the blueprint does not declare was accepted")
	}
	if len(log.boots) != 0 {
		t.Fatalf("boot calls = %v, want none before a refused slot", log.boots)
	}
	if _, err := a.Replace(SlotAct, probeEffector{log}); err != nil {
		t.Fatalf("replace after a refused slot: %v", err)
	}
	if !slices.Equal(log.boots, []string{"Act"}) {
		t.Errorf("boot calls = %v, want the organ booted once, at its real slot", log.boots)
	}
}

// TestReplaceAfterCloseRefusesOrgan: a closed agent cannot use a port, so it
// says so instead of quietly consuming one that was already brought up.
func TestReplaceAfterCloseRefusesOrgan(t *testing.T) {
	o, _ := bootableOrgans()
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	log := &lifecycleLog{}
	if _, err := a.Replace(SlotAct, probeEffector{log}); !errors.Is(err, ErrCellClosed) {
		t.Fatalf("Replace after Close = %v, want it to report the agent closed", err)
	}
	if len(log.boots) != 0 {
		t.Errorf("boot calls = %v, want none for a closed agent", log.boots)
	}
}

// TestCombinatorsDoNotForwardBoot: a combinator returns a port value, and the
// framework asserts the lifecycle on the value it is handed — so members behind
// GuardStack/Fallback* are never booted through it. A member that must be brought
// up boots before it is composed; this is the contract, not an oversight.
func TestCombinatorsDoNotForwardBoot(t *testing.T) {
	o, _ := bootableOrgans()
	first, second := &lifecycleLog{}, &lifecycleLog{}
	o.Sandbox = GuardStack(probeSandbox{first}, probeSandbox{second})
	a, err := New(Blueprint{Organs: o, Config: Config{}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer a.Close()
	if len(first.boots)+len(second.boots) != 0 {
		t.Fatalf("member boots = %v / %v, want none through a combinator", first.boots, second.boots)
	}
}

// TestOrganFilledCoversEveryBlueprintSlot: the api answers "is this slot
// wired?" for every edge the blueprint declares. A slot added to the blueprint
// without an entry here would be reported unwired forever — this guard turns
// that drift into a build-time test failure.
func TestOrganFilledCoversEveryBlueprintSlot(t *testing.T) {
	for _, wp := range Connectome() {
		if _, ok := organFilled[wp.ID]; !ok {
			t.Errorf("blueprint slot %s (%s) has no organFilled entry", wp.ID, wp.Name)
		}
	}
}
