// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// tools.go — ToolSpec → chat tools. Rebuilt from p.Tools on every round:
// which tools are visible this round is what p.Tools itself says
// (BeforeStimulate may rewrite it wholesale), and a tool table cached at
// construction would silently drop the rewrite for the price of one JSON
// unmarshal.
package brain

import (
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go/v3"

	"github.com/qyiun666/meowire/internal/nerve"
)

// toolParams maps the round's tool list onto the chat wire. ToolSpec.Input is
// JSON Schema text and the SDK wants the object; a malformed schema is a
// wiring mistake and fails the round loudly. ToolSpec.Output has no seat in
// the chat protocol (it accepts no output schema), so it is folded into the
// description — still text the model reads.
func toolParams(p *nerve.Prompt) ([]openai.ChatCompletionToolUnionParam, error) {
	tools := make([]openai.ChatCompletionToolUnionParam, 0, len(p.Tools))
	for _, spec := range p.Tools {
		schema, err := toolSchema(spec)
		if err != nil {
			return nil, err
		}
		tools = append(tools, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        spec.Name,
			Description: openai.String(describeTool(spec)),
			Parameters:  schema,
		}))
	}
	return tools, nil
}

// toolSchema parses one tool's JSON Schema text — the one parsing step both
// wires share (each protocol wants its own wrapper around the same object).
func toolSchema(spec nerve.ToolSpec) (openai.FunctionParameters, error) {
	schema := openai.FunctionParameters{}
	if spec.Input != "" {
		if err := json.Unmarshal([]byte(spec.Input), &schema); err != nil {
			return nil, fmt.Errorf("brain: tool %q input schema: %w", spec.Name, err)
		}
	}
	return schema, nil
}

// describeTool collapses one tool declaration; the output description rides
// along because the protocol has no field for it.
func describeTool(spec nerve.ToolSpec) string {
	if spec.Output == "" {
		return spec.Desc
	}
	return spec.Desc + "\nReturns: " + spec.Output
}
