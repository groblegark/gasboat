package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newTestStateManager(t *testing.T) *StateManager {
	t.Helper()
	sm, err := NewStateManager(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("NewStateManager: %v", err)
	}
	return sm
}

func TestNewStateManager_CreatesEmptyState(t *testing.T) {
	sm := newTestStateManager(t)

	if msgs := sm.AllDecisionMessages(); len(msgs) != 0 {
		t.Errorf("expected 0 decision messages, got %d", len(msgs))
	}
	if msgs := sm.AllChatMessages(); len(msgs) != 0 {
		t.Errorf("expected 0 chat messages, got %d", len(msgs))
	}
	if cards := sm.AllAgentCards(); len(cards) != 0 {
		t.Errorf("expected 0 agent cards, got %d", len(cards))
	}
	if d, ok := sm.GetDashboard(); ok {
		t.Errorf("expected no dashboard, got %+v", d)
	}
	if id := sm.GetLastEventID(); id != "" {
		t.Errorf("expected empty last event ID, got %q", id)
	}
}

func TestNewStateManager_LoadsExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	data := StateData{
		DecisionMessages: map[string]MessageRef{
			"kd-1": {ChannelID: "C1", Timestamp: "1.1"},
		},
		ChatMessages: map[string]MessageRef{},
		AgentCards:   map[string]MessageRef{},
		LastEventID:  "evt-42",
	}
	raw, _ := json.Marshal(data)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	sm, err := NewStateManager(path)
	if err != nil {
		t.Fatalf("NewStateManager: %v", err)
	}

	ref, ok := sm.GetDecisionMessage("kd-1")
	if !ok {
		t.Fatal("expected decision message kd-1 to exist")
	}
	if ref.ChannelID != "C1" || ref.Timestamp != "1.1" {
		t.Errorf("unexpected ref: %+v", ref)
	}
	if id := sm.GetLastEventID(); id != "evt-42" {
		t.Errorf("expected last event ID evt-42, got %q", id)
	}
}

func TestNewStateManager_ReturnsErrorOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{bad json"), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	_, err := NewStateManager(path)
	if err == nil {
		t.Fatal("expected error on corrupt state file")
	}
}

// --- Decision Messages ---

func TestDecisionMessages_SetGetRemove(t *testing.T) {
	sm := newTestStateManager(t)

	ref := MessageRef{ChannelID: "C1", Timestamp: "1.1", Agent: "agent-1"}
	if err := sm.SetDecisionMessage("kd-1", ref); err != nil {
		t.Fatalf("SetDecisionMessage: %v", err)
	}

	got, ok := sm.GetDecisionMessage("kd-1")
	if !ok {
		t.Fatal("expected decision message to exist")
	}
	if got != ref {
		t.Errorf("got %+v, want %+v", got, ref)
	}

	// Not found.
	_, ok = sm.GetDecisionMessage("kd-missing")
	if ok {
		t.Error("expected missing message to not exist")
	}

	if err := sm.RemoveDecisionMessage("kd-1"); err != nil {
		t.Fatalf("RemoveDecisionMessage: %v", err)
	}

	_, ok = sm.GetDecisionMessage("kd-1")
	if ok {
		t.Error("expected removed message to not exist")
	}
}

func TestAllDecisionMessages_ReturnsCopy(t *testing.T) {
	sm := newTestStateManager(t)

	ref1 := MessageRef{ChannelID: "C1", Timestamp: "1.1"}
	ref2 := MessageRef{ChannelID: "C2", Timestamp: "2.2"}
	_ = sm.SetDecisionMessage("kd-1", ref1)
	_ = sm.SetDecisionMessage("kd-2", ref2)

	all := sm.AllDecisionMessages()
	if len(all) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(all))
	}

	// Modifying the returned map should not affect the state.
	delete(all, "kd-1")
	if _, ok := sm.GetDecisionMessage("kd-1"); !ok {
		t.Error("modifying returned map should not affect state")
	}
}

// --- Agent Cards ---

func TestAgentCards_SetGetRemove(t *testing.T) {
	sm := newTestStateManager(t)

	ref := MessageRef{ChannelID: "C1", Timestamp: "1.1"}
	if err := sm.SetAgentCard("bot-1", ref); err != nil {
		t.Fatalf("SetAgentCard: %v", err)
	}

	got, ok := sm.GetAgentCard("bot-1")
	if !ok {
		t.Fatal("expected agent card to exist")
	}
	if got != ref {
		t.Errorf("got %+v, want %+v", got, ref)
	}

	_, ok = sm.GetAgentCard("missing")
	if ok {
		t.Error("expected missing agent card to not exist")
	}

	if err := sm.RemoveAgentCard("bot-1"); err != nil {
		t.Fatalf("RemoveAgentCard: %v", err)
	}

	_, ok = sm.GetAgentCard("bot-1")
	if ok {
		t.Error("expected removed agent card to not exist")
	}
}

// --- Dashboard ---

func TestDashboard_SetGet(t *testing.T) {
	sm := newTestStateManager(t)

	_, ok := sm.GetDashboard()
	if ok {
		t.Error("expected no dashboard initially")
	}

	ref := DashboardRef{ChannelID: "C1", Timestamp: "1.1", LastHash: "abc"}
	if err := sm.SetDashboard(ref); err != nil {
		t.Fatalf("SetDashboard: %v", err)
	}

	got, ok := sm.GetDashboard()
	if !ok {
		t.Fatal("expected dashboard to exist")
	}
	if got.ChannelID != "C1" || got.Timestamp != "1.1" || got.LastHash != "abc" {
		t.Errorf("unexpected dashboard ref: %+v", got)
	}

	// Modifying the returned pointer should not affect the state.
	got.LastHash = "modified"
	got2, _ := sm.GetDashboard()
	if got2.LastHash != "abc" {
		t.Error("modifying returned pointer should not affect state")
	}
}

// --- SSE Event ID ---

func TestLastEventID_SetGet(t *testing.T) {
	sm := newTestStateManager(t)

	if id := sm.GetLastEventID(); id != "" {
		t.Errorf("expected empty, got %q", id)
	}

	if err := sm.SetLastEventID("evt-100"); err != nil {
		t.Fatalf("SetLastEventID: %v", err)
	}

	if id := sm.GetLastEventID(); id != "evt-100" {
		t.Errorf("expected evt-100, got %q", id)
	}
}

// --- Persistence ---

func TestState_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	sm1, err := NewStateManager(path)
	if err != nil {
		t.Fatalf("NewStateManager: %v", err)
	}

	_ = sm1.SetDecisionMessage("kd-1", MessageRef{ChannelID: "C1", Timestamp: "1.1"})
	_ = sm1.SetAgentCard("bot-1", MessageRef{ChannelID: "C2", Timestamp: "2.2"})
	_ = sm1.SetDashboard(DashboardRef{ChannelID: "C3", Timestamp: "3.3", LastHash: "h1"})
	_ = sm1.SetLastEventID("evt-99")
	_ = sm1.SetChatMessage("kd-c1", MessageRef{ChannelID: "C4", Timestamp: "4.4"})

	// Create a new instance from the same file.
	sm2, err := NewStateManager(path)
	if err != nil {
		t.Fatalf("NewStateManager (reload): %v", err)
	}

	if ref, ok := sm2.GetDecisionMessage("kd-1"); !ok || ref.ChannelID != "C1" {
		t.Errorf("decision message not persisted: ok=%v ref=%+v", ok, ref)
	}
	if ref, ok := sm2.GetAgentCard("bot-1"); !ok || ref.ChannelID != "C2" {
		t.Errorf("agent card not persisted: ok=%v ref=%+v", ok, ref)
	}
	if d, ok := sm2.GetDashboard(); !ok || d.ChannelID != "C3" {
		t.Errorf("dashboard not persisted: ok=%v d=%+v", ok, d)
	}
	if id := sm2.GetLastEventID(); id != "evt-99" {
		t.Errorf("last event ID not persisted: %q", id)
	}
	if ref, ok := sm2.GetChatMessage("kd-c1"); !ok || ref.ChannelID != "C4" {
		t.Errorf("chat message not persisted: ok=%v ref=%+v", ok, ref)
	}
}

func TestState_RemoveDecisionMessage_Persists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	sm1, _ := NewStateManager(path)
	_ = sm1.SetDecisionMessage("kd-1", MessageRef{ChannelID: "C1", Timestamp: "1.1"})
	_ = sm1.RemoveDecisionMessage("kd-1")

	sm2, _ := NewStateManager(path)
	if _, ok := sm2.GetDecisionMessage("kd-1"); ok {
		t.Error("removed decision message should not persist")
	}
}

func TestNewStateManager_NilMapsInFileAreInitialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Write a state file with null maps.
	if err := os.WriteFile(path, []byte(`{"last_event_id":"x"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	sm, err := NewStateManager(path)
	if err != nil {
		t.Fatalf("NewStateManager: %v", err)
	}

	// Operations on the nil-initialized maps should not panic.
	if err := sm.SetDecisionMessage("kd-1", MessageRef{ChannelID: "C1"}); err != nil {
		t.Fatalf("SetDecisionMessage on nil map: %v", err)
	}
	if err := sm.SetChatMessage("kd-2", MessageRef{ChannelID: "C2"}); err != nil {
		t.Fatalf("SetChatMessage on nil map: %v", err)
	}
	if err := sm.SetAgentCard("bot-1", MessageRef{ChannelID: "C3"}); err != nil {
		t.Fatalf("SetAgentCard on nil map: %v", err)
	}
}
