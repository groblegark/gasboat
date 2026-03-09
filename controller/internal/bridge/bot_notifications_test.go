package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// slackCall records a single Slack API call made to the fake server.
type slackCall struct {
	Method   string
	Channel  string
	Text     string
	ThreadTS string
}

// capturingSlackServer returns an httptest.Server that records all Slack API
// calls (chat.postMessage, chat.update) for assertion.
func capturingSlackServer(t *testing.T) (*httptest.Server, *[]slackCall, *sync.Mutex) {
	t.Helper()
	var calls []slackCall
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls = append(calls, slackCall{
			Method:   strings.TrimPrefix(r.URL.Path, "/"),
			Channel:  r.FormValue("channel"),
			Text:     r.FormValue("text"),
			ThreadTS: r.FormValue("thread_ts"),
		})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1111.2222", "message_ts": "1111.2222"})
	}))
	return srv, &calls, &mu
}

func getCalls(calls *[]slackCall, mu *sync.Mutex) []slackCall {
	mu.Lock()
	defer mu.Unlock()
	return append([]slackCall{}, *calls...)
}

// --- NotifyAgentCrash tests ---

func TestNotifyAgentCrash_Basic(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:       "bd-crash-1",
		Assignee: "my-agent",
		Fields: map[string]string{
			"agent_state": "failed",
			"pod_phase":   "failed",
			"pod_name":    "agent-pod-xyz",
		},
	}

	err := bot.NotifyAgentCrash(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 Slack call, got %d", len(got))
	}
	if got[0].Method != "chat.postMessage" {
		t.Errorf("expected chat.postMessage, got %s", got[0].Method)
	}
	if got[0].Channel != "C-test" {
		t.Errorf("expected channel C-test, got %s", got[0].Channel)
	}
	if !strings.Contains(got[0].Text, "Agent crashed") {
		t.Errorf("expected text to contain 'Agent crashed', got %s", got[0].Text)
	}
}

func TestNotifyAgentCrash_FallsBackToTitle(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:    "bd-crash-2",
		Title: "fallback-agent",
		Fields: map[string]string{
			"agent_state": "failed",
		},
	}

	err := bot.NotifyAgentCrash(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 Slack call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "fallback-agent") {
		t.Errorf("expected text to contain 'fallback-agent', got %s", got[0].Text)
	}
}

func TestNotifyAgentCrash_FallsBackToID(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:     "bd-crash-3",
		Fields: map[string]string{},
	}

	err := bot.NotifyAgentCrash(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "bd-crash-3") {
		t.Errorf("expected text to contain bead ID, got %s", got[0].Text)
	}
}

func TestNotifyAgentCrash_ThreadBoundAgent(t *testing.T) {
	daemon := newMockDaemon()
	// Seed an agent bead with thread binding fields.
	daemon.mu.Lock()
	daemon.beads["my-thread-agent"] = &beadsapi.BeadDetail{
		ID:    "bd-agent-thread",
		Title: "my-thread-agent",
		Type:  "agent",
		Fields: map[string]string{
			"agent":                "my-thread-agent",
			"slack_thread_channel": "C-thread-chan",
			"slack_thread_ts":      "9999.0001",
		},
	}
	daemon.mu.Unlock()

	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-default"

	bead := BeadEvent{
		ID:       "bd-crash-thread",
		Assignee: "my-thread-agent",
		Fields:   map[string]string{},
	}

	err := bot.NotifyAgentCrash(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if got[0].Channel != "C-thread-chan" {
		t.Errorf("expected crash posted to thread-bound channel C-thread-chan, got %s", got[0].Channel)
	}
	if got[0].ThreadTS != "9999.0001" {
		t.Errorf("expected thread_ts 9999.0001, got %s", got[0].ThreadTS)
	}
}

// --- NotifyJackOn tests ---

func TestNotifyJackOn_Basic(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:       "bd-jack-1",
		Title:    "Jack for deploy",
		Assignee: "deploy-agent",
		Fields: map[string]string{
			"target": "production-db",
			"ttl":    "30m",
			"reason": "Schema migration",
		},
	}

	err := bot.NotifyJackOn(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if got[0].Method != "chat.postMessage" {
		t.Errorf("expected chat.postMessage, got %s", got[0].Method)
	}
	if !strings.Contains(got[0].Text, "Jack raised") {
		t.Errorf("expected text to contain 'Jack raised', got %s", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "production-db") {
		t.Errorf("expected text to contain target, got %s", got[0].Text)
	}
}

func TestNotifyJackOn_NoOptionalFields(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:     "bd-jack-2",
		Fields: map[string]string{"target": "staging-api"},
	}

	err := bot.NotifyJackOn(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
}

// --- NotifyJackOnBatch tests ---

func TestNotifyJackOnBatch_Basic(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	beads := []BeadEvent{
		{ID: "jack-1", Title: "J1", Fields: map[string]string{"target": "svc-a"}, Assignee: "agent-1"},
		{ID: "jack-2", Title: "J2", Fields: map[string]string{"target": "svc-b"}},
		{ID: "jack-3", Title: "J3", Fields: map[string]string{"target": "svc-c"}},
	}

	err := bot.NotifyJackOnBatch(context.Background(), beads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if got[0].Channel != "C-test" {
		t.Errorf("expected channel C-test, got %s", got[0].Channel)
	}
	if !strings.Contains(got[0].Text, "3 additional jacks raised") {
		t.Errorf("expected batch count in text, got %s", got[0].Text)
	}
}

func TestNotifyJackOnBatch_MoreThanFive(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	beads := make([]BeadEvent, 8)
	for i := range beads {
		beads[i] = BeadEvent{
			ID:     "jack-batch-" + string(rune('a'+i)),
			Fields: map[string]string{"target": "svc"},
		}
	}

	err := bot.NotifyJackOnBatch(context.Background(), beads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "8 additional jacks raised") {
		t.Errorf("expected 8 in text, got %s", got[0].Text)
	}
}

func TestNotifyJackOnBatch_FewerThanFive(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	beads := []BeadEvent{
		{ID: "j1", Fields: map[string]string{"target": "a"}},
		{ID: "j2", Fields: map[string]string{"target": "b"}},
	}

	err := bot.NotifyJackOnBatch(context.Background(), beads)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "2 additional jacks raised") {
		t.Errorf("expected 2 in text, got %s", got[0].Text)
	}
}

// --- NotifyJackOff tests ---

func TestNotifyJackOff_Basic(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:       "bd-jack-off-1",
		Title:    "Jack lowered",
		Assignee: "ops-agent",
		Fields: map[string]string{
			"target": "production-db",
			"reason": "Migration complete",
		},
	}

	err := bot.NotifyJackOff(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "Jack lowered") {
		t.Errorf("expected text to contain 'Jack lowered', got %s", got[0].Text)
	}
}

func TestNotifyJackOff_NoOptionalFields(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:     "bd-jack-off-2",
		Fields: map[string]string{"target": "staging"},
	}

	err := bot.NotifyJackOff(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
}

// --- NotifyJackExpired tests ---

func TestNotifyJackExpired_Basic(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:       "bd-jack-exp-1",
		Title:    "Expired jack",
		Assignee: "ops-agent",
		Fields: map[string]string{
			"target": "production-db",
			"reason": "TTL exceeded",
		},
	}

	err := bot.NotifyJackExpired(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
	if !strings.Contains(got[0].Text, "Jack expired") {
		t.Errorf("expected text to contain 'Jack expired', got %s", got[0].Text)
	}
	if !strings.Contains(got[0].Text, "bd-jack-exp-1") {
		t.Errorf("expected text to contain bead ID, got %s", got[0].Text)
	}
}

func TestNotifyJackExpired_NoOptionalFields(t *testing.T) {
	daemon := newMockDaemon()
	srv, calls, mu := capturingSlackServer(t)
	defer srv.Close()

	bot := newTestBot(daemon, srv)
	bot.channel = "C-test"

	bead := BeadEvent{
		ID:     "bd-jack-exp-2",
		Fields: map[string]string{"target": "staging"},
	}

	err := bot.NotifyJackExpired(context.Background(), bead)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := getCalls(calls, mu)
	if len(got) != 1 {
		t.Fatalf("expected 1 call, got %d", len(got))
	}
}
