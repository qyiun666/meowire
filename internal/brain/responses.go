// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// responses.go — the Responses API wire (Mode 2): the second transport the
// mode enum can select. The contract is the chat wire's — stateless render
// (one Prompt is the round's whole truth, the pairing rebuilt from
// ToolResults alone), same Sink deal, same single-layer retry — only the
// protocol shapes differ. v3.61.0 ships no responses accumulator, and none
// is needed: the terminal event carries the whole Response.
package brain

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/qyiun666/meowire/internal/nerve"
)

// thinkResponses renders the Prompt into one Responses API request and folds
// the response back into a Decision.
func (b *Brain) thinkResponses(ctx context.Context, p *nerve.Prompt) (*nerve.Decision, error) {
	tools, err := responseToolParams(p)
	if err != nil {
		return nil, err
	}
	params := responses.ResponseNewParams{
		Instructions: openai.String(systemText(p)),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: responseInput(p),
		},
		Model: openai.ResponsesModel(b.model),
		Tools: tools,
		// Store is off: the kernel never sends previous_response_id and
		// rebuilds the whole input each round, so server-side retention would
		// only pile up copies nobody reads back.
		Store: openai.Bool(false),
	}
	if b.stream {
		return streamResponse(ctx, b.client, params, sinkFrom(ctx))
	}
	resp, err := b.client.Responses.New(ctx, params)
	if err != nil {
		return nil, wrapErr(err, "responses")
	}
	return responseDecisionOf(resp)
}

// responseInput builds the input items in conversation order: the round's
// stimulus first (constant across an invocation's rounds, so the prefix
// stays stable and a provider's prefix cache keeps eating it), then one
// function_call + function_call_output pair per ToolResult, self-paired by
// call id — the same single-batch rebuild the chat wire performs. Denied
// calls never enter ToolResults, so they are absent here and need no
// placeholder. Call arguments are not part of a ToolResult and are not
// reconstructed: the model re-reads its own intent from the call name and
// the feedback.
func responseInput(p *nerve.Prompt) responses.ResponseInputParam {
	items := responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(p.Input, responses.EasyInputMessageRoleUser),
	}
	for _, tr := range p.ToolResults {
		items = append(items,
			responses.ResponseInputItemUnionParam{
				OfFunctionCall: &responses.ResponseFunctionToolCallParam{
					CallID:    tr.ID,
					Name:      tr.Name,
					Arguments: "{}",
				},
			},
			responses.ResponseInputItemUnionParam{
				OfFunctionCallOutput: &responses.ResponseInputItemFunctionCallOutputParam{
					CallID: openai.String(tr.ID),
					Output: responses.ResponseInputItemFunctionCallOutputOutputUnionParam{
						// Err non-empty means the call failed; its text is the reply.
						OfString: openai.String(cmp.Or(tr.Err, tr.Result)),
					},
				},
			},
		)
	}
	return items
}

// responseToolParams maps the round's tool list onto the Responses function
// tool shape — same every-round rebuild (BeforeStimulate may rewrite p.Tools
// wholesale), same schema parsing, same Output-in-description fold and same
// empty-list omission (nil, so the field is not sent) as the chat wire.
func responseToolParams(p *nerve.Prompt) ([]responses.ToolUnionParam, error) {
	if len(p.Tools) == 0 {
		return nil, nil
	}
	tools := make([]responses.ToolUnionParam, 0, len(p.Tools))
	for _, spec := range p.Tools {
		schema, err := toolSchema(spec)
		if err != nil {
			return nil, err
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        spec.Name,
				Description: openai.String(describeTool(spec)),
				Parameters:  schema,
			},
		})
	}
	return tools, nil
}

// streamResponse consumes the Responses event stream. Text deltas reach the
// sink as they arrive — before the output membrane rules, the same
// stream-and-correct contract as the chat wire — and the terminal event
// (completed, failed or incomplete) carries the whole Response, so the
// fold-back needs no accumulator and nothing is merged fragment by fragment.
// Function-call argument deltas stream too, but the sink is a text channel:
// calls arrive whole, inside the terminal event.
func streamResponse(ctx context.Context, client openai.Client, params responses.ResponseNewParams, sink Sink) (*nerve.Decision, error) {
	stream := client.Responses.NewStreaming(ctx, params)
	// Close on every exit: the SSE contract says an iteration that stops
	// before Next returns false must Close, and an error event returning
	// early would otherwise hold the connection. closeOnce makes the
	// success path's own close a no-op.
	defer stream.Close()
	var resp *responses.Response
	for stream.Next() {
		ev := stream.Current()
		switch ev.Type {
		case "response.output_text.delta":
			if sink != nil && ev.Delta != "" {
				sink(ev.Delta)
			}
		case "response.completed", "response.failed", "response.incomplete":
			r := ev.Response
			resp = &r
		case "error":
			return nil, fmt.Errorf("brain: responses stream failed (%s): %s", ev.Code, ev.Message)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, wrapErr(err, "responses")
	}
	if resp == nil {
		return nil, errors.New("brain: responses stream ended without a terminal event")
	}
	return responseDecisionOf(resp)
}

// responseDecisionOf folds one Response back into the loop's shape. A failed
// terminal state is an error — the round dies honestly instead of folding an
// empty decision, the same fail-fast a permanent chat error gets from its
// status text. An incomplete response keeps its partial text, the same way
// the chat wire lets a length-cut finish through. Function-call arguments
// pass through verbatim; Usage stays nil when the wire carried zeros — the
// event stream then reports nothing instead of a fake EventUsage.
func responseDecisionOf(r *responses.Response) (*nerve.Decision, error) {
	if r.Status == responses.ResponseStatusFailed {
		e := r.Error
		return nil, fmt.Errorf("brain: responses run failed (code %q): %s", e.Code, e.Message)
	}
	dec := &nerve.Decision{}
	for _, item := range r.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "output_text" {
					dec.Text += part.Text
				}
			}
		case "function_call":
			dec.ToolCalls = append(dec.ToolCalls, nerve.ToolCall{
				ID:   item.CallID,
				Name: item.Name,
				Args: item.Arguments.OfString,
			})
		}
	}
	if u := r.Usage; u.InputTokens != 0 || u.OutputTokens != 0 || u.TotalTokens != 0 {
		dec.Usage = &nerve.Usage{
			Prompt:     int(u.InputTokens),
			Completion: int(u.OutputTokens),
			Total:      int(u.TotalTokens),
		}
	}
	return dec, nil
}
