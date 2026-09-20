// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// wiring_free_test.go — the acceptance criterion read mechanically. The host
// writes organs and consumes an iterator; nothing between two organs is its
// job. A kernel that ever needs a channel, a goroutine or a queue receive to be
// assembled has grown wiring the host must hand-write, and this file is where
// that regression shows up.
package meowire_test

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	meowire "github.com/qyiun666/meowire/api"
)

// hostFile is the acceptance face this guard scans: organs written with public
// names only, one Stimulate consumed with a range loop.
const hostFile = "public_organs_test.go"

// TestHostSurfaceIsWiringFree scans the host-written acceptance file for the
// wiring a host should never have to write. The forbidden tokens are assembled
// at runtime so this file does not contain what it forbids.
func TestHostSurfaceIsWiringFree(t *testing.T) {
	src, err := os.ReadFile(hostFile)
	if err != nil {
		t.Fatalf("read %s: %v", hostFile, err)
	}
	text := string(src)
	for _, forbidden := range []string{"ch" + "an ", "g" + "o func", "<" + "-", "sync." + "WaitGroup"} {
		if i := strings.Index(text, forbidden); i >= 0 {
			t.Fatalf("%s: host-side wiring found (%q at offset %d): organs alone must assemble the kernel", hostFile, forbidden, i)
		}
	}
}

// organsFields are every slot a host may declare on the assembly. The list is
// the kernel's whole optional-and-required surface: six host ports, the brain
// parameters, and the fixed parts — and no delivery organ between agents: an
// agent that wants another's work is created by the host, not addressed by the
// kernel.
var organsFields = []string{
	"ID", "Brain", "Act", "Closer", "Hooks", "Sandbox", "Budget", "Mem",
	"System", "Methods", "Tools", "Context", "Identity",
}

// TestOrgansDeclaresNoInterAgentSlot fails the moment an organ addressed to
// another agent is added back to the assembly surface.
func TestOrgansDeclaresNoInterAgentSlot(t *testing.T) {
	typ := reflect.TypeFor[meowire.Organs]()
	got := make([]string, typ.NumField())
	for i := range got {
		got[i] = typ.Field(i).Name
	}
	slices.Sort(got)
	want := slices.Clone(organsFields)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("Organs fields = %v, want exactly %v", got, want)
	}
}
