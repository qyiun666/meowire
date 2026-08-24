// Copyright (c) 2026 qyiun666
// SPDX-License-Identifier: MIT

// card_test.go — AgentCard capability declaration tests.
package meowire

import (
	"encoding/json"
	"testing"
)

// TestAgentCard: the card is valid JSON carrying the agent name, identity
// description and the Methods projection as skills.
func TestAgentCard(t *testing.T) {
	o := fullOrgans()
	o.ID = "research-agent"
	o.Identity = "a research specialist"
	o.Methods = []MethodSpec{
		{Name: "web_search", Desc: "search the web", Input: "query", Output: "results"},
		{Name: "code_review", Desc: "review code"},
	}

	doc, err := AgentCard(o)
	if err != nil {
		t.Fatalf("AgentCard: %v", err)
	}
	if !json.Valid(doc) {
		t.Fatal("AgentCard output is not valid JSON")
	}
	var card agentCard
	if err := json.Unmarshal(doc, &card); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if card.Name != "research-agent" {
		t.Errorf("name = %q, want research-agent", card.Name)
	}
	if card.Description != "a research specialist" {
		t.Errorf("description = %q, want identity text", card.Description)
	}
	if len(card.Skills) != 2 {
		t.Fatalf("skills = %d, want 2", len(card.Skills))
	}
	if card.Skills[0].Name != "web_search" || card.Skills[0].Input != "query" ||
		card.Skills[0].Output != "results" {
		t.Errorf("skill[0] = %+v, want web_search with input/output", card.Skills[0])
	}
	if card.Skills[1].Name != "code_review" || card.Skills[1].Input != "" {
		t.Errorf("skill[1] = %+v, want code_review with empty input", card.Skills[1])
	}
}

// TestAgentCardDefaults: empty ID falls back to "agent"; empty Methods yields
// an empty (non-nil) skills list.
func TestAgentCardDefaults(t *testing.T) {
	doc, err := AgentCard(fullOrgans())
	if err != nil {
		t.Fatalf("AgentCard: %v", err)
	}
	var card agentCard
	if err := json.Unmarshal(doc, &card); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if card.Name != "agent" {
		t.Errorf("name = %q, want agent default", card.Name)
	}
	if card.Skills == nil {
		t.Error("skills should be an empty list, not null")
	}
}
