// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// names.go — the discipline every enum name table is read through.
package nerve

import "slices"

// nameOf returns the wire name a table holds for v, or "" when v is outside the
// table. An unnamed enum value is not a value this build knows, which is a
// reason to refuse, never a reason to guess a default.
func nameOf[T ~int](names []string, v T) string {
	if int(v) < 0 || int(v) >= len(names) {
		return ""
	}
	return names[v]
}

// valueOfName resolves a wire name back to its enum value, reporting whether the
// table carries it at all; an unknown name is refused by the caller rather than
// read as the zero value.
func valueOfName[T ~int](names []string, name string) (T, bool) {
	i := slices.Index(names, name)
	if i < 0 {
		return 0, false
	}
	return T(i), true
}
