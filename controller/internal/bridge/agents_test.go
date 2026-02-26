package bridge

import (
	"context"
	"log/slog"
	"sync"
	"testing"
)

// mockAgentNotifier records calls to NotifyAgentCrash, NotifyAgentSpawn, and NotifyAgentState.
type mockAgentNotifier struct {
	mu           sync.Mutex
	crashes      []BeadEvent
	spawns       []BeadEvent
	stateChanges []BeadEvent
}

func (m *mockAgentNotifier) NotifyAgentCrash(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.crashes = append(m.crashes, bead)
	return nil
}

func (m *mockAgentNotifier) NotifyAgentSpawn(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spawns = append(m.spawns, bead)
}

func (m *mockAgentNotifier) NotifyAgentState(_ context.Context, bead BeadEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateChanges = append(m.stateChanges, bead)
}

func (m *mockAgentNotifier) getCrashes() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.crashes...)
}

func (m *mockAgentNotifier) getSpawns() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.spawns...)
}

func (m *mockAgentNotifier) getStateChanges() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.stateChanges...)
}

func TestAgents_HandleClosed_CrashNotification(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-agent bead should be ignored.
	nonAgent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-1",
		Type: "decision",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})
	a.handleClosed(context.Background(), nonAgent)
	if len(notif.getCrashes()) != 0 {
		t.Fatal("non-agent bead should not trigger crash notification")
	}

	// Agent bead closing with agent_state=done should be ignored.
	doneAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-1",
		Type:     "agent",
		Assignee: "gasboat/crew/test-bot",
		Fields: map[string]string{
			"agent_state": "done",
		},
	})
	a.handleClosed(context.Background(), doneAgent)
	if len(notif.getCrashes()) != 0 {
		t.Fatal("agent with state=done should not trigger crash notification")
	}

	// Agent bead closing with agent_state=failed should trigger notification.
	crashedAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-2",
		Type:     "agent",
		Title:    "crew-gasboat-crew-test-bot",
		Assignee: "gasboat/crew/test-bot",
		Fields: map[string]string{
			"agent_state": "failed",
			"pod_name":    "crew-gasboat-crew-test-bot-xyz",
		},
	})
	a.handleClosed(context.Background(), crashedAgent)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected 1 crash notification, got %d", len(crashes))
	}
	if crashes[0].ID != "agent-2" {
		t.Errorf("expected bead ID agent-2, got %s", crashes[0].ID)
	}
	if crashes[0].Assignee != "gasboat/crew/test-bot" {
		t.Errorf("expected assignee gasboat/crew/test-bot, got %s", crashes[0].Assignee)
	}
}

func TestAgents_HandleUpdated_PodPhaseFailed(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Agent updated with pod_phase=failed should trigger notification.
	failedPod := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-3",
		Type:     "agent",
		Assignee: "gasboat/crew/worker-1",
		Fields: map[string]string{
			"agent_state": "working",
			"pod_phase":   "failed",
			"pod_name":    "crew-gasboat-crew-worker-1-abc",
		},
	})
	a.handleUpdated(context.Background(), failedPod)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected 1 crash notification, got %d", len(crashes))
	}
	if crashes[0].ID != "agent-3" {
		t.Errorf("expected bead ID agent-3, got %s", crashes[0].ID)
	}
}

func TestAgents_Deduplication(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	crashEvent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-4",
		Type:     "agent",
		Assignee: "gasboat/crew/bot-a",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})

	// First call: should notify.
	a.handleClosed(context.Background(), crashEvent)
	// Second call (e.g., from SSE reconnect): should be deduplicated.
	a.handleClosed(context.Background(), crashEvent)
	// Third call via updated handler: still deduplicated.
	a.handleUpdated(context.Background(), crashEvent)

	crashes := notif.getCrashes()
	if len(crashes) != 1 {
		t.Fatalf("expected exactly 1 crash notification (dedup), got %d", len(crashes))
	}
}

func TestAgents_NilNotifier(t *testing.T) {
	a := NewAgents(AgentsConfig{
		Notifier: nil,
		Logger:   slog.Default(),
	})

	crashEvent := marshalSSEBeadPayload(BeadEvent{
		ID:   "agent-5",
		Type: "agent",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	})

	// Should not panic even with nil notifier.
	a.handleClosed(context.Background(), crashEvent)
}

// TestAgents_HandleCreated verifies that agent bead creation fires
// NotifyAgentSpawn for agent beads and is skipped for non-agent beads.
func TestAgents_HandleCreated(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	// Non-agent bead should be ignored.
	nonAgent := marshalSSEBeadPayload(BeadEvent{
		ID:   "dec-10",
		Type: "decision",
	})
	a.handleCreated(context.Background(), nonAgent)
	if len(notif.getSpawns()) != 0 {
		t.Fatal("non-agent bead should not trigger spawn notification")
	}

	// Agent bead creation should trigger spawn notification.
	agentBead := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-10",
		Type:     "agent",
		Title:    "crew-gasboat-crew-builder",
		Assignee: "gasboat/crew/builder",
	})
	a.handleCreated(context.Background(), agentBead)

	spawns := notif.getSpawns()
	if len(spawns) != 1 {
		t.Fatalf("expected 1 spawn notification, got %d", len(spawns))
	}
	if spawns[0].ID != "agent-10" {
		t.Errorf("expected bead ID agent-10, got %s", spawns[0].ID)
	}
	if spawns[0].Assignee != "gasboat/crew/builder" {
		t.Errorf("expected assignee gasboat/crew/builder, got %s", spawns[0].Assignee)
	}
}

// TestAgents_HandleClosed_NormalCompletion verifies that a normally completed
// agent bead (not failed) triggers NotifyAgentState with state "done".
func TestAgents_HandleClosed_NormalCompletion(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	doneAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-11",
		Type:     "agent",
		Assignee: "gasboat/crew/finisher",
		Fields: map[string]string{
			"agent_state": "done",
		},
	})
	a.handleClosed(context.Background(), doneAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("normal completion should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "done" {
		t.Errorf("expected agent_state=done, got %q", changes[0].Fields["agent_state"])
	}
}

// TestAgents_HandleClosed_NoState verifies that an agent bead closing without
// an explicit agent_state gets defaulted to "done".
func TestAgents_HandleClosed_NoState(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	closedAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-12",
		Type:     "agent",
		Assignee: "gasboat/crew/silent",
		Fields:   map[string]string{},
	})
	a.handleClosed(context.Background(), closedAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("normal close should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "done" {
		t.Errorf("expected agent_state defaulted to done, got %q", changes[0].Fields["agent_state"])
	}
}

// TestAgents_HandleUpdated_StateChange verifies that non-crash state changes
// (e.g. spawning→working) trigger NotifyAgentState, not NotifyAgentCrash.
func TestAgents_HandleUpdated_StateChange(t *testing.T) {
	notif := &mockAgentNotifier{}
	a := NewAgents(AgentsConfig{
		Notifier: notif,
		Logger:   slog.Default(),
	})

	workingAgent := marshalSSEBeadPayload(BeadEvent{
		ID:       "agent-6",
		Type:     "agent",
		Assignee: "gasboat/crew/runner",
		Fields: map[string]string{
			"agent_state": "working",
			"pod_phase":   "running",
		},
	})
	a.handleUpdated(context.Background(), workingAgent)

	if len(notif.getCrashes()) != 0 {
		t.Error("working state should not trigger crash notification")
	}
	changes := notif.getStateChanges()
	if len(changes) != 1 {
		t.Fatalf("expected 1 state change notification, got %d", len(changes))
	}
	if changes[0].Fields["agent_state"] != "working" {
		t.Errorf("expected agent_state=working, got %q", changes[0].Fields["agent_state"])
	}
}
