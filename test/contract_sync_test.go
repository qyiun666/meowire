// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// contract_sync_test.go — contract documentation drift guard.
//
// The EventKind enum in internal/nerve/event.go is the single source of
// truth. Any change to it must be mirrored in the api aliases and the
// contract documents; this test fails otherwise, so a drift cannot
// silently reach a release. reference-host.md is intentionally excluded:
// its event switch is an example (hosts handle only the events they care
// about), reviewed manually via the agent.md checklist.
package meowire_test

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// phaseHostPort is the blueprint phase marking a host-implemented port.
const phaseHostPort = 2

const (
	eventGoPath  = "../internal/nerve/event.go"
	stateGoPath  = "../internal/nerve/state.go"
	portGoPath   = "../internal/nerve/port.go"
	typesGoPath  = "../api/types.go"
	nerveAgentMD = "../internal/nerve/agent.md"
	hostMD       = "../host-integration.md"
	hostENMD     = "../host-integration.en.md"
)

func readContractFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// iotaConstNames extracts the constant names from the iota block in src whose
// declaration line contains marker (e.g. "EventKind = iota").
func iotaConstNames(t *testing.T, src string, marker string) []string {
	t.Helper()
	blockRe := regexp.MustCompile(`const \(\n([\s\S]*?)\n\s*\)`)
	nameRe := regexp.MustCompile(`^\s*(\w+)`)
	for _, m := range blockRe.FindAllStringSubmatch(src, -1) {
		if !strings.Contains(m[1], marker) {
			continue
		}
		var names []string
		for line := range strings.SplitSeq(m[1], "\n") {
			if name := nameRe.FindStringSubmatch(line); len(name) == 2 {
				names = append(names, name[1])
			}
		}
		return names
	}
	t.Fatalf("no %s iota const block found", marker)
	return nil
}

// eventKindsFromSource extracts the EventKind constants from the iota block.
func eventKindsFromSource(t *testing.T) []string {
	return iotaConstNames(t, readContractFile(t, eventGoPath), "EventKind = iota")
}

// loopStatesFromSource extracts the LoopState constants from the iota block.
func loopStatesFromSource(t *testing.T) []string {
	return iotaConstNames(t, readContractFile(t, stateGoPath), "LoopState = iota")
}

// assertSameSet fails unless got and want hold the same names (order-insensitive).
func assertSameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	sort := func(s []string) []string {
		s = slices.Clone(s)
		slices.Sort(s)
		return s
	}
	if !slices.Equal(sort(got), sort(want)) {
		t.Fatalf("%s drift: got %v, want %v", what, sort(got), sort(want))
	}
}

// assertCovered fails unless every kind appears as a table first column.
func assertCovered(t *testing.T, what, doc string, kinds []string) {
	t.Helper()
	rows := map[string]bool{}
	for _, m := range regexp.MustCompile("(?m)^\\| `(\\w+)` \\|").FindAllStringSubmatch(doc, -1) {
		rows[m[1]] = true
	}
	var missing []string
	for _, k := range kinds {
		if !rows[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s missing table rows for %v", what, missing)
	}
}

func TestEventKindContractSynced(t *testing.T) {
	kinds := eventKindsFromSource(t)

	// api/types.go aliases must mirror the nerve enum exactly.
	types := readContractFile(t, typesGoPath)
	aliasRe := regexp.MustCompile(`(\w+)\s+EventKind = nerve\.(\w+)`)
	var lhs, rhs []string
	for _, m := range aliasRe.FindAllStringSubmatch(types, -1) {
		lhs = append(lhs, m[1])
		rhs = append(rhs, m[2])
	}
	assertSameSet(t, "api/types.go EventKind aliases (left side)", lhs, kinds)
	assertSameSet(t, "api/types.go EventKind aliases (right side)", rhs, kinds)

	// internal/nerve/agent.md must enumerate every kind on the EventKind line.
	agentMD := readContractFile(t, nerveAgentMD)
	m := regexp.MustCompile("`EventKind`:\\s*([\\w/]+)").FindStringSubmatch(agentMD)
	if len(m) == 0 {
		t.Fatal("no `EventKind`: line found in internal/nerve/agent.md")
	}
	assertSameSet(t, "internal/nerve/agent.md `EventKind` line", strings.Split(m[1], "/"), kinds)

	// host-integration guides must cover every kind in a table first column.
	assertCovered(t, "host-integration.md event table", readContractFile(t, hostMD), kinds)
	assertCovered(t, "host-integration.en.md event table", readContractFile(t, hostENMD), kinds)
}

// TestLoopStateContractSynced guards the LoopState enum (single source of
// truth: internal/nerve/loop.go) against drift in the api aliases, the
// agent.md enumeration, and the host-integration guides (state names appear
// in the EventState row and sequence descriptions).
func TestLoopStateContractSynced(t *testing.T) {
	states := loopStatesFromSource(t)

	// api/types.go aliases must mirror the nerve enum exactly.
	types := readContractFile(t, typesGoPath)
	aliasRe := regexp.MustCompile(`(\w+)\s+LoopState = nerve\.(\w+)`)
	var lhs, rhs []string
	for _, m := range aliasRe.FindAllStringSubmatch(types, -1) {
		lhs = append(lhs, m[1])
		rhs = append(rhs, m[2])
	}
	assertSameSet(t, "api/types.go LoopState aliases (left side)", lhs, states)
	assertSameSet(t, "api/types.go LoopState aliases (right side)", rhs, states)

	// internal/nerve/agent.md must enumerate every state on the LoopState line.
	agentMD := readContractFile(t, nerveAgentMD)
	m := regexp.MustCompile("`LoopState`:\\s*([\\w/]+)").FindStringSubmatch(agentMD)
	if len(m) == 0 {
		t.Fatal("no `LoopState`: line found in internal/nerve/agent.md")
	}
	assertSameSet(t, "internal/nerve/agent.md `LoopState` line", strings.Split(m[1], "/"), states)

	// host-integration guides must mention every state's lowercase name
	// (idle/thinking/acting/paused/waiting/done/error in the EventState row).
	for _, path := range []string{hostMD, hostENMD} {
		doc := readContractFile(t, path)
		for _, s := range states {
			lower := strings.ToLower(strings.TrimPrefix(s, "State"))
			if !strings.Contains(doc, lower) {
				t.Fatalf("%s does not mention loop state %q", path, s)
			}
		}
	}
}

// TestWaitKindContractSynced guards the suspension-flavour enum (single source
// of truth: internal/nerve/port.go). A flavour the api surface cannot name is a
// suspension a host cannot read Session.Kind against, so it cannot tell which
// field of a Resume Response its answer belongs in.
func TestWaitKindContractSynced(t *testing.T) {
	kinds := iotaConstNames(t, readContractFile(t, portGoPath), "WaitKind = iota")
	if len(kinds) == 0 {
		t.Fatal("no WaitKind flavours found")
	}

	types := readContractFile(t, typesGoPath)
	aliasRe := regexp.MustCompile(`(\w+)\s+WaitKind = nerve\.(\w+)`)
	var lhs, rhs []string
	for _, m := range aliasRe.FindAllStringSubmatch(types, -1) {
		lhs = append(lhs, m[1])
		rhs = append(rhs, m[2])
	}
	assertSameSet(t, "api/types.go WaitKind aliases (left side)", lhs, kinds)
	assertSameSet(t, "api/types.go WaitKind aliases (right side)", rhs, kinds)
}

// TestRequiredPortNamesSyncedWithGuides pins the port names the guides
// advertise to the blueprint: the Organs table of each host-integration guide
// carries one required row per required phase-2 slot, and the rows name exactly
// those ports. A port added to the blueprint without an organ row (or the
// reverse, or a rename that keeps the count) fails here — counts alone would
// let a swap of two ports through.
func TestRequiredPortNamesSyncedWithGuides(t *testing.T) {
	var names []string
	for _, wp := range meowire.Connectome() {
		if wp.Phase == phaseHostPort && wp.Required {
			names = append(names, wp.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("blueprint advertises no required host port — the phase filter is wrong")
	}

	for _, tc := range []struct{ path, mark string }{
		{hostMD, "是"},
		{hostENMD, "yes"},
	} {
		table := organsTable(t, tc.path)
		rowRe := regexp.MustCompile("(?m)^\\| `(\\w+)`.*\\| \\*\\*" + tc.mark + "\\*\\* \\|$")
		var got []string
		for _, m := range rowRe.FindAllStringSubmatch(table, -1) {
			got = append(got, m[1])
		}
		// `ID` is a required assembly field, not a port: its row carries the
		// same required mark as a port row but must not enter the port set
		// (the blueprint's phase-2 slots have no ID entry to pair with).
		got = slices.DeleteFunc(got, func(s string) bool { return s == "ID" })
		assertSameSet(t, tc.path+" required organ rows", got, names)
	}
}

// organsTable returns the Organs assembly table section of a host guide (from
// its "## 3." heading to the next top-level heading).
func organsTable(t *testing.T, path string) string {
	t.Helper()
	doc := readContractFile(t, path)
	start := strings.Index(doc, "\n## 3.")
	if start < 0 {
		t.Fatalf("%s has no '## 3.' section", path)
	}
	rest := doc[start+1:]
	if end := strings.Index(rest[3:], "\n## "); end >= 0 {
		return rest[:end+3]
	}
	return rest
}
