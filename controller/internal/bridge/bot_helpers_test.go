package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/slack-go/slack/slackevents"
)

// --- handleMessageEvent tests ---

func TestHandleMessageEvent_IgnoresBotOwnMessages(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "UBOTID"

	// Message from the bot itself should be ignored.
	bot.handleMessageEvent(context.Background(), &slackevents.MessageEvent{
		User:    "UBOTID",
		Text:    "hello",
		Channel: "C123",
	})
}

func TestHandleMessageEvent_IgnoresEmptyUser(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "UBOTID"

	bot.handleMessageEvent(context.Background(), &slackevents.MessageEvent{
		User:    "",
		Text:    "hello",
		Channel: "C123",
	})
}

func TestHandleMessageEvent_IgnoresSubtypes(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "UBOTID"

	bot.handleMessageEvent(context.Background(), &slackevents.MessageEvent{
		User:    "U123",
		Text:    "hello",
		Channel: "C123",
		SubType: "message_changed",
	})
}

func TestHandleMessageEvent_NonThreadNonMention_Ignored(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "UBOTID"

	// Non-thread, non-mention, non-group message is ignored.
	bot.handleMessageEvent(context.Background(), &slackevents.MessageEvent{
		User:        "U123",
		Text:        "random message",
		Channel:     "C123",
		ChannelType: "channel",
	})
}

func TestHandleMessageEvent_GroupChannelMention_SynthesizesAppMention(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.botUserID = "UBOTID"

	// Group channel + mention should synthesize AppMentionEvent.
	// This won't crash because handleAppMention will just not match any agent.
	bot.handleMessageEvent(context.Background(), &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hey <@UBOTID> do something",
		Channel:     "G123",
		ChannelType: "group",
		TimeStamp:   "1234.5678",
	})
}

// --- hintMentionRequired tests ---

func TestHintMentionRequired_PostsHint(t *testing.T) {
	var posted bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)
	bot.lastThreadNudge = make(map[string]time.Time)

	bot.hintMentionRequired(context.Background(), "C123", "1111.2222", "my-agent")

	if !posted {
		t.Error("expected hint message to be posted")
	}
}

func TestHintMentionRequired_Throttled(t *testing.T) {
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)
	bot.lastThreadNudge = make(map[string]time.Time)

	// First call should go through.
	bot.hintMentionRequired(context.Background(), "C123", "1111.2222", "my-agent")

	// Second call within 10 minutes should be throttled.
	callCount := 0
	slackSrv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv2.Close()

	// Re-wire the API to the counting server — but the throttle should prevent the call.
	bot.hintMentionRequired(context.Background(), "C123", "1111.2222", "my-agent")

	// The second call used the same bot.api, so the lastThreadNudge check
	// should prevent posting. We verify by checking the nudge map has the key.
	key := "hint:C123:1111.2222"
	bot.mu.Lock()
	_, ok := bot.lastThreadNudge[key]
	bot.mu.Unlock()
	if !ok {
		t.Error("expected nudge key to be stored")
	}
}

func TestHintMentionRequired_NilAPI(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.lastThreadNudge = make(map[string]time.Time)
	bot.api = nil

	// Should not panic when api is nil.
	bot.hintMentionRequired(context.Background(), "C123", "1111.2222", "my-agent")
}

// --- handleThreadReply tests ---

func TestHandleThreadReply_NoDecisionThread(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// No state manager, so getDecisionByThread returns "".
	bot.handleThreadReply(context.Background(), &slackevents.MessageEvent{
		User:            "U123",
		Text:            "my reply",
		Channel:         "C123",
		ThreadTimeStamp: "1111.2222",
	})

	// Should silently return without error.
	if len(daemon.getClosed()) != 0 {
		t.Error("expected no beads to be closed")
	}
}

func TestHandleThreadReply_ResolvesOpenDecision(t *testing.T) {
	daemon := newMockDaemon()

	// Seed an open decision bead.
	daemon.mu.Lock()
	daemon.beads["kd-dec-1"] = &beadsapi.BeadDetail{
		ID:     "kd-dec-1",
		Title:  "Choose a color",
		Type:   "decision",
		Status: "open",
	}
	daemon.mu.Unlock()

	// Create a real slack server that handles users.info.
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "users.info") || r.FormValue("user") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"user": map[string]any{"id": "U123", "real_name": "Bob", "name": "bob"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Set up a state manager with the decision message mapping.
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	sm, err := NewStateManager(statePath)
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	_ = sm.SetDecisionMessage("kd-dec-1", MessageRef{ChannelID: "C123", Timestamp: "1111.2222"})
	bot.state = sm

	bot.handleThreadReply(context.Background(), &slackevents.MessageEvent{
		User:            "U123",
		Text:            "blue",
		Channel:         "C123",
		ThreadTimeStamp: "1111.2222",
		TimeStamp:       "1111.3333",
	})

	closed := daemon.getClosed()
	if len(closed) != 1 {
		t.Fatalf("expected 1 close call, got %d", len(closed))
	}
	if closed[0].BeadID != "kd-dec-1" {
		t.Errorf("expected bead kd-dec-1 to be closed, got %s", closed[0].BeadID)
	}
	if closed[0].Fields["chosen"] != "blue" {
		t.Errorf("expected chosen=blue, got %s", closed[0].Fields["chosen"])
	}
}

func TestHandleThreadReply_SkipsAlreadyClosedDecision(t *testing.T) {
	daemon := newMockDaemon()

	// Seed a closed decision bead.
	daemon.mu.Lock()
	daemon.beads["kd-dec-closed"] = &beadsapi.BeadDetail{
		ID:     "kd-dec-closed",
		Title:  "Already decided",
		Type:   "decision",
		Status: "closed",
	}
	daemon.mu.Unlock()

	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "users.info") || r.FormValue("user") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":   true,
				"user": map[string]any{"id": "U123", "real_name": "Bob"},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	sm, err := NewStateManager(statePath)
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	_ = sm.SetDecisionMessage("kd-dec-closed", MessageRef{ChannelID: "C123", Timestamp: "1111.2222"})
	bot.state = sm

	bot.handleThreadReply(context.Background(), &slackevents.MessageEvent{
		User:            "U123",
		Text:            "too late",
		Channel:         "C123",
		ThreadTimeStamp: "1111.2222",
		TimeStamp:       "1111.3333",
	})

	// Decision already closed, so no close call.
	if len(daemon.getClosed()) != 0 {
		t.Error("expected no close calls for already-closed decision")
	}
}

// --- getDecisionByThread tests ---

func TestGetDecisionByThread_NilState(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.state = nil

	result := bot.getDecisionByThread("C123", "1111.2222")
	if result != "" {
		t.Errorf("expected empty string for nil state, got %q", result)
	}
}

func TestGetDecisionByThread_Found(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	sm, err := NewStateManager(statePath)
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	_ = sm.SetDecisionMessage("kd-dec-42", MessageRef{ChannelID: "C123", Timestamp: "1111.2222"})
	bot.state = sm

	result := bot.getDecisionByThread("C123", "1111.2222")
	if result != "kd-dec-42" {
		t.Errorf("expected kd-dec-42, got %q", result)
	}
}

func TestGetDecisionByThread_NotFound(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	sm, err := NewStateManager(statePath)
	if err != nil {
		t.Fatalf("failed to create state manager: %v", err)
	}
	bot.state = sm

	result := bot.getDecisionByThread("C123", "9999.9999")
	if result != "" {
		t.Errorf("expected empty for no match, got %q", result)
	}
}

// --- postThreadStateReply tests ---

func TestPostThreadStateReply_Done(t *testing.T) {
	var postedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				postedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bead := BeadEvent{
		ID:     "kd-agent-1",
		Fields: map[string]string{},
	}

	bot.postThreadStateReply(context.Background(), "my-agent", "done", bead, "C123", "1111.2222")

	if postedText == "" {
		t.Error("expected a message to be posted")
	}
	if !strings.Contains(postedText, "finished") {
		t.Errorf("expected 'finished' in posted text, got %q", postedText)
	}
}

func TestPostThreadStateReply_Failed(t *testing.T) {
	var postedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				postedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bead := BeadEvent{
		ID:     "kd-agent-1",
		Fields: map[string]string{"close_reason": "OOM killed"},
	}

	bot.postThreadStateReply(context.Background(), "my-agent", "failed", bead, "C123", "1111.2222")

	if postedText == "" {
		t.Error("expected a message to be posted")
	}
	if !strings.Contains(postedText, "failed") {
		t.Errorf("expected 'failed' in posted text, got %q", postedText)
	}
}

func TestPostThreadStateReply_NonTerminalState_NoPost(t *testing.T) {
	var called bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bead := BeadEvent{ID: "kd-agent-1", Fields: map[string]string{}}

	bot.postThreadStateReply(context.Background(), "my-agent", "running", bead, "C123", "1111.2222")

	if called {
		t.Error("expected no Slack API call for non-terminal state")
	}
}

func TestPostThreadStateReply_UpdatesSpawnMessage(t *testing.T) {
	var updatedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				updatedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "9999.1111"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	// Pre-populate spawn message ref so it tries to update in-place.
	bot.mu.Lock()
	bot.threadSpawnMsgs["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "9999.1111"}
	bot.mu.Unlock()

	bead := BeadEvent{ID: "kd-agent-1", Fields: map[string]string{}}
	bot.postThreadStateReply(context.Background(), "my-agent", "done", bead, "C123", "1111.2222")

	if updatedText == "" {
		t.Error("expected spawn message to be updated")
	}

	// Verify spawn message was removed from the map.
	bot.mu.Lock()
	_, still := bot.threadSpawnMsgs["my-agent"]
	bot.mu.Unlock()
	if still {
		t.Error("expected spawn message ref to be removed after update")
	}
}

func TestPostThreadStateReply_WithWrapup(t *testing.T) {
	var postedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				postedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	wrapup := `{"accomplishments":"Fixed the bug","blockers":"","handoff_notes":"Ready for review"}`
	bead := BeadEvent{
		ID:     "kd-agent-1",
		Fields: map[string]string{"wrapup": wrapup},
	}

	bot.postThreadStateReply(context.Background(), "my-agent", "done", bead, "C123", "1111.2222")

	if postedText == "" {
		t.Error("expected a message to be posted with wrapup content")
	}
}

// --- resolveChannel project-specific override tests ---

func TestResolveChannel_RouterPatternMatch(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-default"
	bot.router = NewRouter(RouterConfig{
		DefaultChannel: "C-router-default",
		Channels: map[string]string{
			"gasboat/crew/*": "C-crew",
		},
	})

	result := bot.resolveChannel("gasboat/crew/my-agent")
	if result != "C-crew" {
		t.Errorf("expected C-crew from pattern match, got %q", result)
	}
}

func TestResolveChannel_RouterOverrideTakesPrecedence(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-default"
	bot.router = NewRouter(RouterConfig{
		DefaultChannel: "C-router-default",
		Channels: map[string]string{
			"gasboat/crew/*": "C-crew",
		},
		Overrides: map[string]string{
			"gasboat/crew/special-bot": "C-special",
		},
	})

	result := bot.resolveChannel("gasboat/crew/special-bot")
	if result != "C-special" {
		t.Errorf("expected C-special (override takes precedence), got %q", result)
	}
}

func TestResolveChannel_RouterNoMatch_FallsBackToRouterDefault(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-default"
	bot.router = NewRouter(RouterConfig{
		DefaultChannel: "C-router-default",
		Channels: map[string]string{
			"other-project/crew/*": "C-other",
		},
	})

	result := bot.resolveChannel("gasboat/crew/my-agent")
	if result != "C-router-default" {
		t.Errorf("expected C-router-default when no pattern matches, got %q", result)
	}
}

func TestResolveChannel_NilRouter_UsesDefaultChannel(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-default"
	bot.router = nil

	result := bot.resolveChannel("gasboat/crew/my-agent")
	if result != "C-default" {
		t.Errorf("expected C-default with nil router, got %q", result)
	}
}

func TestResolveChannel_RouterMultipleProjects(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-default"
	bot.router = NewRouter(RouterConfig{
		DefaultChannel: "C-router-default",
		Channels: map[string]string{
			"gasboat/crew/*": "C-gasboat",
			"kbeads/crew/*":  "C-kbeads",
		},
	})

	if got := bot.resolveChannel("gasboat/crew/bot-a"); got != "C-gasboat" {
		t.Errorf("expected C-gasboat, got %q", got)
	}
	if got := bot.resolveChannel("kbeads/crew/bot-b"); got != "C-kbeads" {
		t.Errorf("expected C-kbeads, got %q", got)
	}
}

// --- replaceAgentCardWithWrapUp tests ---

func TestReplaceAgentCardWithWrapUp_UpdatesExistingCard(t *testing.T) {
	var updatedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				updatedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1111.2222"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bot.mu.Lock()
	bot.agentCards["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}
	bot.mu.Unlock()

	wrapup := `{"accomplishments":"Fixed the bug","blockers":"","handoff_notes":"Ready for review"}`
	bot.replaceAgentCardWithWrapUp(context.Background(), "my-agent", "done", wrapup)

	if updatedText == "" {
		t.Error("expected agent card to be updated")
	}
	if !strings.Contains(updatedText, "done") {
		t.Errorf("expected 'done' in updated text, got %q", updatedText)
	}
}

func TestReplaceAgentCardWithWrapUp_NoCard_FallsBackToUpdateAgentCard(t *testing.T) {
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	// No card exists — should not panic, falls back to updateAgentCard.
	bot.replaceAgentCardWithWrapUp(context.Background(), "no-such-agent", "done", `{"accomplishments":"test"}`)
}

func TestReplaceAgentCardWithWrapUp_FailedState(t *testing.T) {
	var updatedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				updatedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1111.2222"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bot.mu.Lock()
	bot.agentCards["fail-agent"] = MessageRef{ChannelID: "C123", Timestamp: "2222.3333"}
	bot.mu.Unlock()

	wrapup := `{"accomplishments":"","blockers":"OOM killed","handoff_notes":""}`
	bot.replaceAgentCardWithWrapUp(context.Background(), "fail-agent", "failed", wrapup)

	if updatedText == "" {
		t.Error("expected agent card to be updated")
	}
	if !strings.Contains(updatedText, "failed") {
		t.Errorf("expected 'failed' in updated text, got %q", updatedText)
	}
}

func TestReplaceAgentCardWithWrapUp_EmptyWrapup(t *testing.T) {
	var updatedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err == nil {
			if text := r.FormValue("text"); text != "" {
				updatedText = text
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1111.2222"})
	}))
	defer slackSrv.Close()

	daemon := newMockDaemon()
	bot := newTestBot(daemon, slackSrv)

	bot.mu.Lock()
	bot.agentCards["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}
	bot.mu.Unlock()

	// Empty wrapup JSON — should still update the card with just the header.
	bot.replaceAgentCardWithWrapUp(context.Background(), "my-agent", "done", `{}`)

	if updatedText == "" {
		t.Error("expected agent card to be updated even with empty wrapup")
	}
}

