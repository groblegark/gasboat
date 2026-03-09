package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

// --- handleClearAgent tests ---

func TestHandleClearAgent_DeletesCardAndCleansState(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Set up state manager.
	tmpDir := t.TempDir()
	sm, err := NewStateManager(filepath.Join(tmpDir, "state.json"))
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	bot.state = sm

	// Pre-populate agent maps.
	bot.mu.Lock()
	bot.agentCards["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}
	bot.agentPending["my-agent"] = 3
	bot.agentState["my-agent"] = "working"
	bot.agentPodName["my-agent"] = "pod-abc"
	bot.agentImageTag["my-agent"] = "v1.0.0"
	bot.agentRole["my-agent"] = "crew"
	bot.mu.Unlock()
	_ = sm.SetAgentCard("my-agent", MessageRef{ChannelID: "C123", Timestamp: "1111.2222"})
	_ = sm.SetThreadAgent("C123", "3333.4444", "my-agent")

	callback := slack.InteractionCallback{
		Channel: slack.Channel{},
		User:    slack.User{ID: "U456"},
	}
	callback.Channel.GroupConversation.Conversation.ID = "C123"

	bot.handleClearAgent(context.Background(), "my-agent", callback)

	// Verify all in-memory maps were cleaned.
	bot.mu.Lock()
	_, hasCard := bot.agentCards["my-agent"]
	_, hasPending := bot.agentPending["my-agent"]
	_, hasState := bot.agentState["my-agent"]
	_, hasPod := bot.agentPodName["my-agent"]
	_, hasTag := bot.agentImageTag["my-agent"]
	_, hasRole := bot.agentRole["my-agent"]
	bot.mu.Unlock()

	if hasCard {
		t.Error("expected agentCards to be cleared")
	}
	if hasPending {
		t.Error("expected agentPending to be cleared")
	}
	if hasState {
		t.Error("expected agentState to be cleared")
	}
	if hasPod {
		t.Error("expected agentPodName to be cleared")
	}
	if hasTag {
		t.Error("expected agentImageTag to be cleared")
	}
	if hasRole {
		t.Error("expected agentRole to be cleared")
	}
}

func TestHandleClearAgent_NoCard_NoOp(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	callback := slack.InteractionCallback{
		User: slack.User{ID: "U456"},
	}

	// Should not panic when no card exists.
	bot.handleClearAgent(context.Background(), "nonexistent-agent", callback)
}

func TestHandleClearAgent_ExtractsShortName(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Store card under short name.
	bot.mu.Lock()
	bot.agentCards["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}
	bot.mu.Unlock()

	callback := slack.InteractionCallback{
		User: slack.User{ID: "U456"},
	}

	// Pass full identity; should extract short name and find the card.
	bot.handleClearAgent(context.Background(), "gasboat/crew/my-agent", callback)

	bot.mu.Lock()
	_, hasCard := bot.agentCards["my-agent"]
	bot.mu.Unlock()

	if hasCard {
		t.Error("expected card to be cleared using short name extracted from full identity")
	}
}

func TestHandleClearAgent_RemovesThreadAgentMapping(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	tmpDir := t.TempDir()
	sm, err := NewStateManager(filepath.Join(tmpDir, "state.json"))
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	bot.state = sm

	// No card, but a thread mapping exists.
	_ = sm.SetThreadAgent("C123", "3333.4444", "my-agent")

	callback := slack.InteractionCallback{
		User: slack.User{ID: "U456"},
	}

	bot.handleClearAgent(context.Background(), "my-agent", callback)

	// Thread mapping should be cleaned up even when no card exists.
	_, ok := sm.GetThreadAgent("C123", "3333.4444")
	if ok {
		t.Error("expected thread agent mapping to be removed")
	}
}

// --- getCoopAgentState tests ---

func TestGetCoopAgentState_ReturnsState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agent" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "working"})
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	state, err := getCoopAgentState(context.Background(), client, srv.URL+"/api/v1")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "working" {
		t.Errorf("expected state=working, got %q", state)
	}
}

func TestGetCoopAgentState_ExitedState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "exited"})
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	state, err := getCoopAgentState(context.Background(), client, srv.URL+"/api/v1")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != "exited" {
		t.Errorf("expected state=exited, got %q", state)
	}
}

func TestGetCoopAgentState_ServerUnreachable(t *testing.T) {
	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := getCoopAgentState(context.Background(), client, "http://127.0.0.1:1/api/v1")

	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestGetCoopAgentState_CancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "working"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	client := &http.Client{Timeout: 3 * time.Second}
	_, err := getCoopAgentState(ctx, client, srv.URL+"/api/v1")

	if err == nil {
		t.Error("expected error for cancelled context")
	}
}

func TestGetCoopAgentState_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	_, err := getCoopAgentState(context.Background(), client, srv.URL+"/api/v1")

	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
}

// --- postCoopKeys tests ---

func TestPostCoopKeys_SendsKeysPayload(t *testing.T) {
	var receivedBody []byte
	var receivedPath string
	var receivedMethod string
	var receivedContentType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedMethod = r.Method
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	postCoopKeys(context.Background(), client, srv.URL+"/api/v1", "Escape")

	if receivedPath != "/api/v1/input/keys" {
		t.Errorf("expected path /api/v1/input/keys, got %s", receivedPath)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", receivedMethod)
	}
	if receivedContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", receivedContentType)
	}

	var payload map[string][]string
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if len(payload["keys"]) != 1 || payload["keys"][0] != "Escape" {
		t.Errorf("expected keys=[Escape], got %v", payload["keys"])
	}
}

func TestPostCoopKeys_MultipleKeys(t *testing.T) {
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	postCoopKeys(context.Background(), client, srv.URL+"/api/v1", "Escape", "Enter")

	var payload map[string][]string
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if len(payload["keys"]) != 2 {
		t.Errorf("expected 2 keys, got %d", len(payload["keys"]))
	}
}

func TestPostCoopKeys_UnreachableServer_NoPanic(t *testing.T) {
	client := &http.Client{Timeout: 100 * time.Millisecond}
	// Should not panic when server is unreachable.
	postCoopKeys(context.Background(), client, "http://127.0.0.1:1/api/v1", "Escape")
}

func TestPostCoopKeys_CancelledContext_NoPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := &http.Client{Timeout: 3 * time.Second}
	postCoopKeys(ctx, client, srv.URL+"/api/v1", "Escape")
}

// --- gracefulShutdownCoop tests ---

func TestGracefulShutdownCoop_ImmediateExit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agent" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "exited"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ok := gracefulShutdownCoop(ctx, srv.URL)
	if !ok {
		t.Error("expected graceful shutdown to succeed when agent is already exited")
	}
}

func TestGracefulShutdownCoop_TransitionsToExited(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agent" && r.Method == http.MethodGet {
			count := callCount.Add(1)
			if count >= 2 {
				_ = json.NewEncoder(w).Encode(map[string]string{"state": "exited"})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]string{"state": "working"})
			}
			return
		}
		// /input/keys endpoint
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ok := gracefulShutdownCoop(ctx, srv.URL)
	if !ok {
		t.Error("expected graceful shutdown to succeed after transition to exited")
	}
	if callCount.Load() < 2 {
		t.Errorf("expected at least 2 state checks, got %d", callCount.Load())
	}
}

func TestGracefulShutdownCoop_ServerUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Use an address that will fail immediately.
	ok := gracefulShutdownCoop(ctx, "http://127.0.0.1:1")
	if ok {
		t.Error("expected false when server is unreachable")
	}
}

func TestGracefulShutdownCoop_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return "working" so it never exits on its own.
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "working"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ok := gracefulShutdownCoop(ctx, srv.URL)
	if ok {
		t.Error("expected false when context is cancelled before agent exits")
	}
}

func TestGracefulShutdownCoop_TrailingSlashInURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/agent" {
			_ = json.NewEncoder(w).Encode(map[string]string{"state": "exited"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// URL with trailing slash should still work.
	ok := gracefulShutdownCoop(ctx, srv.URL+"/")
	if !ok {
		t.Error("expected success with trailing slash in URL")
	}
}
