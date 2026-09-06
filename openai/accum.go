// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// accum.go — streaming tool-call delta accumulation state machines (one per
// wire). OpenAI SSE streams deliver tool calls as delta shards keyed by an
// index (ID/Name only on the first shard, arguments appended segment by
// segment); both accumulators reassemble them and emit them in order.

package openai

import (
	"slices"
	"strings"

	meowire "github.com/qyiun666/meowire/api"
)

// accToolCall is one reassembling unit (delta shards arrive keyed by index).
type accToolCall struct {
	id   string
	name string
	args strings.Builder
}

// accToolCalls reassembles chat-wire delta tool calls: when Index is
// present the shards accumulate under it; gateways that omit Index (shards
// without a key) allocate arrival-order slots — shards carrying ID/Name
// open a new call, bare argument segments append to the latest one. Both
// shapes guarantee no tool call is lost.
type accToolCalls struct {
	byIndex map[int]*accToolCall
	seq     int // arrival-order slot allocator for index-less deltas
	last    int // latest index-less slot (argument-only shards append here)
}

func (a *accToolCalls) accum(deltas []chatDeltaToolCall) {
	for _, d := range deltas {
		if a.byIndex == nil {
			a.byIndex = make(map[int]*accToolCall)
		}
		idx := -1
		if d.Index != nil {
			idx = *d.Index
		}
		if idx < 0 {
			if d.ID != "" || d.Function.Name != "" {
				for {
					idx = a.seq
					a.seq++
					if _, ok := a.byIndex[idx]; !ok {
						break
					}
				}
				a.last = idx
			} else {
				// argument-only shard: append to the latest call (zero value
				// → slot 0, fine for single-call streams)
				idx = a.last
			}
		}
		c := a.byIndex[idx]
		if c == nil {
			c = &accToolCall{}
			a.byIndex[idx] = c
		}
		if d.ID != "" {
			c.id = d.ID
		}
		if d.Function.Name != "" {
			c.name = d.Function.Name
		}
		c.args.WriteString(d.Function.Arguments)
	}
}

// finish emits the reassembled calls sorted by index (nil when nothing
// accumulated = the no-tool-calls semantics).
func (a *accToolCalls) finish() []meowire.ToolCall {
	if len(a.byIndex) == 0 {
		return nil
	}
	idxs := make([]int, 0, len(a.byIndex))
	for i := range a.byIndex {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	out := make([]meowire.ToolCall, 0, len(idxs))
	for _, i := range idxs {
		c := a.byIndex[i]
		out = append(out, meowire.ToolCall{ID: c.id, Name: c.name, Args: c.args.String()})
	}
	return out
}

// respCallAcc is one responses-wire reassembling unit.
type respCallAcc struct {
	id   string
	name string
	args strings.Builder
}

// accRespCalls reassembles responses-wire streamed function calls:
// output_item.added/done bind ID/Name, function_call_arguments.delta
// appends arguments — keyed by output_index, emitted in arrival order. The
// server always sends output_index for multi-call streams; when absent the
// events degrade to the single slot (index 0), which stays correct for
// single-call streams.
type accRespCalls struct {
	byIndex map[int]*respCallAcc
	order   []int
}

func (a *accRespCalls) slot(index int) *respCallAcc {
	if a.byIndex == nil {
		a.byIndex = make(map[int]*respCallAcc)
	}
	c, ok := a.byIndex[index]
	if !ok {
		c = &respCallAcc{}
		a.byIndex[index] = c
		a.order = append(a.order, index)
	}
	return c
}

// bind records item-level info (ID prefers call_id); idempotent — bound
// values are never overwritten.
func (a *accRespCalls) bind(index int, item *respOutputItem) {
	c := a.slot(index)
	if c.id == "" {
		c.id = firstNonEmpty(item.CallID, item.ID)
	}
	if c.name == "" {
		c.name = item.Name
	}
}

// setArgs replaces the accumulated increments with the complete arguments
// (the output_item.done tail; idempotent).
func (a *accRespCalls) setArgs(index int, args string) {
	c := a.slot(index)
	c.args.Reset()
	c.args.WriteString(args)
}

func (a *accRespCalls) accum(index int, args string) {
	if args == "" {
		return
	}
	a.slot(index).args.WriteString(args)
}

// finish emits in arrival order; nameless slots are skipped (noise-index
// defense).
func (a *accRespCalls) finish() []meowire.ToolCall {
	out := make([]meowire.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		c := a.byIndex[idx]
		if c.name == "" {
			continue
		}
		out = append(out, meowire.ToolCall{ID: c.id, Name: c.name, Args: c.args.String()})
	}
	return out
}
