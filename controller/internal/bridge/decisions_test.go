package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"log/slog"

	"github.com/nats-io/nats.go"

	"gasboat/controller/internal/client"
)

// mockDaemon implements BeadClient for testing.
type mockDaemon struct {
	mu       sync.Mutex
	beads    map[string]*client.BeadDetail
	closed   []closeCall
	getCalls int
}

type closeCall struct {
	BeadID string
	Fields map[string]string
}

func newMockDaemon() *mockDaemon {
	return &mockDaemon{
		beads: make(map[string]*client.BeadDetail),
	}
}

func (m *mockDaemon) GetBead(_ context.Context, beadID string) (*client.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if b, ok := m.beads[beadID]; ok {
		return b, nil
	}
	return &client.BeadDetail{ID: beadID}, nil
}

func (m *mockDaemon) CloseBead(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, closeCall{BeadID: beadID, Fields: fields})
	return nil
}

func (m *mockDaemon) getClosed() []closeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]closeCall{}, m.closed...)
}

func (m *mockDaemon) getGetCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getCalls
}

// mockNotifier records calls to NotifyDecision and UpdateDecision.
type mockNotifier struct {
	mu      sync.Mutex
	created []BeadEvent
	updated []updateCall
}

type updateCall struct {
	BeadID string
	Chosen string
}

func (m *mockNotifier) NotifyDecision(_ context.Context, bead BeadEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, bead)
	return nil
}

func (m *mockNotifier) UpdateDecision(_ context.Context, beadID, chosen string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updated = append(m.updated, updateCall{beadID, chosen})
	return nil
}

func (m *mockNotifier) getCreated() []BeadEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BeadEvent{}, m.created...)
}

func (m *mockNotifier) getUpdated() []updateCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]updateCall{}, m.updated...)
}

func TestDecisions_HandleCreated(t *testing.T) {
	d := &Decisions{
		notifier: &mockNotifier{},
		logger:   slog.Default(),
	}

	// Non-decision bead should be ignored.
	nonDecision, _ := json.Marshal(BeadEvent{
		ID:   "abc",
		Type: "agent",
	})
	d.handleCreated(context.Background(), &nats.Msg{Data: nonDecision})

	mn := d.notifier.(*mockNotifier)
	if len(mn.getCreated()) != 0 {
		t.Fatal("non-decision bead should not trigger notification")
	}

	// Decision bead should trigger notification.
	decision, _ := json.Marshal(BeadEvent{
		ID:       "dec-1",
		Type:     "decision",
		Title:    "Pick a color",
		Assignee: "crew-town-crew-hq",
		Fields: map[string]string{
			"question": "What color?",
			"options":  `["red","blue"]`,
		},
	})
	d.handleCreated(context.Background(), &nats.Msg{Data: decision})

	created := mn.getCreated()
	if len(created) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(created))
	}
	if created[0].ID != "dec-1" {
		t.Errorf("expected bead ID dec-1, got %s", created[0].ID)
	}
	if created[0].Assignee != "crew-town-crew-hq" {
		t.Errorf("expected assignee crew-town-crew-hq, got %s", created[0].Assignee)
	}
}

func TestDecisions_HandleClosed_NudgesCoop(t *testing.T) {
	// Set up a fake coop server that records nudge calls.
	var nudgeReceived sync.Mutex
	var nudgeMessage string
	coopServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/nudge" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			nudgeReceived.Lock()
			nudgeMessage = body["message"]
			nudgeReceived.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer coopServer.Close()

	// Set up a mock daemon that returns the agent bead with coop_url.
	daemon := newMockDaemon()
	daemon.beads["crew-town-crew-hq"] = &client.BeadDetail{
		ID: "crew-town-crew-hq",
		Fields: map[string]string{
			"coop_url": coopServer.URL,
		},
	}
	notif := &mockNotifier{}

	d := &Decisions{
		daemon:   daemon,
		notifier: notif,
		logger:   slog.Default(),
	}

	closedEvent, _ := json.Marshal(BeadEvent{
		ID:       "dec-1",
		Type:     "decision",
		Assignee: "crew-town-crew-hq",
		Fields: map[string]string{
			"chosen":    "blue",
			"rationale": "it's calming",
		},
	})
	d.handleClosed(context.Background(), &nats.Msg{Data: closedEvent})

	// Verify nudge was sent to coop.
	time.Sleep(50 * time.Millisecond) // give async processing a moment
	nudgeReceived.Lock()
	msg := nudgeMessage
	nudgeReceived.Unlock()

	if msg == "" {
		t.Fatal("expected coop nudge, got none")
	}
	if msg != "Decision resolved: blue — it's calming" {
		t.Errorf("unexpected nudge message: %s", msg)
	}

	// Verify notifier.UpdateDecision was called.
	updated := notif.getUpdated()
	if len(updated) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(updated))
	}
	if updated[0].BeadID != "dec-1" || updated[0].Chosen != "blue" {
		t.Errorf("unexpected update call: %+v", updated[0])
	}
}

func TestDecisions_HandleClosed_NoAssignee(t *testing.T) {
	d := &Decisions{
		notifier: &mockNotifier{},
		logger:   slog.Default(),
	}

	// Decision closed without assignee — should log warning but not panic.
	closedEvent, _ := json.Marshal(BeadEvent{
		ID:   "dec-2",
		Type: "decision",
		Fields: map[string]string{
			"chosen": "yes",
		},
	})
	d.handleClosed(context.Background(), &nats.Msg{Data: closedEvent})
	// No panic = pass.
}
