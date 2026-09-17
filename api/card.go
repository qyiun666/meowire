// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// card.go — Agent Card: A2A-style machine-readable capability declaration.
//
// The A2A protocol (Agent2Agent, Linux Foundation) lets agents discover each
// other's capabilities via a JSON "Agent Card" published at a well-known
// URL. meowire's Methods projection (gene projection, describes only) is the
// natural seed for such a card: AgentCard derives it entirely from the
// assembly (Organs) — no registration, no runtime state.
package meowire

import (
	"cmp"
	"encoding/json"
)

// agentCard is the JSON shape of the A2A-style capability card. It exposes
// the core A2A fields (name, description, skills); url/authentication are
// host-domain deployment concerns and are intentionally omitted.
type agentCard struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Skills      []skillCard `json:"skills"`
}

// skillCard is one declared capability (projected from a MethodSpec).
type skillCard struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
}

// cardOf projects an assembly into its capability card. It is the single
// source of what counts as a skill: the Methods projection, read the same way
// by Agent.Skills and by the skill routing index.
func cardOf(o Organs) agentCard {
	card := agentCard{
		Name:        cmp.Or(o.ID, "agent"),
		Description: o.Identity,
		Skills:      make([]skillCard, 0, len(o.Methods)),
	}
	for _, m := range o.Methods {
		card.Skills = append(card.Skills, skillCard{
			Name:        m.Name,
			Description: m.Desc,
			Input:       m.Input,
			Output:      m.Output,
		})
	}
	return card
}

// AgentCard renders the host assembly as an A2A-style capability card
// (indented JSON): the agent ID, its identity description, and the built-in
// Methods projection as the skills list. Hosts publish the output at
// /.well-known/agent-card.json so other agents can discover this agent's
// capabilities without calling it.
func AgentCard(o Organs) ([]byte, error) {
	return json.MarshalIndent(cardOf(o), "", "  ")
}
