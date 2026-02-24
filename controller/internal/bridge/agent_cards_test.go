package bridge

import (
	"testing"
)

func TestBuildAgentCardBlocks_NoPending(t *testing.T) {
	b := &Bot{
		channel: "C123",
	}

	blocks := b.buildAgentCardBlocks("gasboat/crew/test-bot", 0)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestBuildAgentCardBlocks_WithPending(t *testing.T) {
	b := &Bot{
		channel: "C123",
	}

	blocks := b.buildAgentCardBlocks("gasboat/crew/test-bot", 3)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestBuildAgentCardBlocks_OnePending(t *testing.T) {
	b := &Bot{
		channel: "C123",
	}

	blocks := b.buildAgentCardBlocks("gasboat/crew/test-bot", 1)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
}

func TestPendingCount_IncrementDecrement(t *testing.T) {
	b := &Bot{
		pendingCount: make(map[string]int),
	}

	// Initially zero.
	if got := b.getPendingCount("agent-a"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	// Increment.
	b.incrementPending("agent-a")
	if got := b.getPendingCount("agent-a"); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}

	b.incrementPending("agent-a")
	if got := b.getPendingCount("agent-a"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}

	// Decrement.
	b.decrementPending("agent-a")
	if got := b.getPendingCount("agent-a"); got != 1 {
		t.Fatalf("expected 1, got %d", got)
	}

	// Decrement to zero.
	b.decrementPending("agent-a")
	if got := b.getPendingCount("agent-a"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	// Decrement past zero should stay at zero.
	b.decrementPending("agent-a")
	if got := b.getPendingCount("agent-a"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestPendingCount_EmptyAgent(t *testing.T) {
	b := &Bot{
		pendingCount: make(map[string]int),
	}

	// Empty agent should not crash.
	b.incrementPending("")
	b.decrementPending("")

	if got := b.getPendingCount(""); got != 0 {
		t.Fatalf("expected 0 for empty agent, got %d", got)
	}
}

func TestPendingCount_IndependentAgents(t *testing.T) {
	b := &Bot{
		pendingCount: make(map[string]int),
	}

	b.incrementPending("agent-a")
	b.incrementPending("agent-a")
	b.incrementPending("agent-b")

	if got := b.getPendingCount("agent-a"); got != 2 {
		t.Fatalf("expected 2 for agent-a, got %d", got)
	}
	if got := b.getPendingCount("agent-b"); got != 1 {
		t.Fatalf("expected 1 for agent-b, got %d", got)
	}
}

func TestGetAgentForDecision_NotFound(t *testing.T) {
	b := &Bot{}

	// No state manager — should return empty string.
	agent := b.getAgentForDecision("dec-1")
	if agent != "" {
		t.Fatalf("expected empty agent, got %q", agent)
	}
}

func TestGetAgentForDecision_FromState(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(dir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	_ = state.SetDecisionMessage("dec-1", MessageRef{
		ChannelID: "C123",
		Timestamp: "123.456",
		Agent:     "gasboat/crew/ops",
	})

	b := &Bot{state: state}

	agent := b.getAgentForDecision("dec-1")
	if agent != "gasboat/crew/ops" {
		t.Fatalf("expected gasboat/crew/ops, got %q", agent)
	}
}

func TestHydrateAgentCards_FromState(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(dir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-populate state with a decision message and agent card.
	_ = state.SetDecisionMessage("dec-1", MessageRef{
		ChannelID: "C123",
		Timestamp: "100.001",
		Agent:     "gasboat/crew/ops",
	})
	_ = state.SetDecisionMessage("dec-2", MessageRef{
		ChannelID: "C123",
		Timestamp: "100.002",
		Agent:     "gasboat/crew/ops",
	})
	_ = state.SetAgentCard("gasboat/crew/ops", MessageRef{
		ChannelID: "C123",
		Timestamp: "50.001",
		Agent:     "gasboat/crew/ops",
	})

	// Create bot — should hydrate from state.
	b := &Bot{
		state:        state,
		channel:      "C123",
		messages:     make(map[string]string),
		agentCards:   make(map[string]agentCardInfo),
		pendingCount: make(map[string]int),
	}

	// Simulate NewBot hydration.
	for id, ref := range state.AllDecisionMessages() {
		b.messages[id] = ref.Timestamp
		if ref.Agent != "" {
			b.pendingCount[ref.Agent]++
		}
	}
	for agent, ref := range state.AllAgentCards() {
		b.agentCards[agent] = agentCardInfo{
			ChannelID: ref.ChannelID,
			Timestamp: ref.Timestamp,
		}
	}

	// Verify pending count hydrated correctly.
	if got := b.getPendingCount("gasboat/crew/ops"); got != 2 {
		t.Fatalf("expected 2 pending decisions, got %d", got)
	}

	// Verify agent card hydrated.
	card, ok := b.agentCards["gasboat/crew/ops"]
	if !ok {
		t.Fatal("expected agent card to be hydrated")
	}
	if card.ChannelID != "C123" || card.Timestamp != "50.001" {
		t.Fatalf("unexpected card: %+v", card)
	}
}

func TestAllAgentCards(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(dir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}

	// Empty — should return empty map.
	cards := state.AllAgentCards()
	if len(cards) != 0 {
		t.Fatalf("expected 0 cards, got %d", len(cards))
	}

	// Add a card and verify.
	_ = state.SetAgentCard("agent-a", MessageRef{
		ChannelID: "C1",
		Timestamp: "1.0",
		Agent:     "agent-a",
	})

	cards = state.AllAgentCards()
	if len(cards) != 1 {
		t.Fatalf("expected 1 card, got %d", len(cards))
	}
	if cards["agent-a"].ChannelID != "C1" {
		t.Errorf("unexpected channel: %s", cards["agent-a"].ChannelID)
	}
}
