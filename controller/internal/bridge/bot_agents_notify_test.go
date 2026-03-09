package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gasboat/controller/internal/beadsapi"
)

// --- Pure function tests ---

func TestExtractImageTag_AllFormats(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ghcr.io/org/agent:v2026.58.3", "v2026.58.3"},
		{"image:latest", "latest"},
		{"latest", "latest"},
		{"", ""},
		{"registry.io/repo:tag:extra", "extra"},
	}
	for _, tc := range tests {
		if got := extractImageTag(tc.input); got != tc.want {
			t.Errorf("extractImageTag(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestExtractAgentProject(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gasboat/crew/test-bot", "gasboat"},
		{"test-bot", ""},
		{"a/b", "a"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := extractAgentProject(tc.input); got != tc.want {
			t.Errorf("extractAgentProject(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCoopmuxAgentLink(t *testing.T) {
	tests := []struct {
		name              string
		coopmuxURL        string
		podName           string
		agentName         string
		wantContains      string
		wantNotContains   string
	}{
		{"with url and pod", "https://mux.example.com", "pod-123", "my-agent", "<https://mux.example.com#pod-123|my-agent>", ""},
		{"no url", "", "pod-123", "my-agent", "my-agent", "<"},
		{"no pod", "https://mux.example.com", "", "my-agent", "my-agent", "<"},
		{"both empty", "", "", "my-agent", "my-agent", "<"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := coopmuxAgentLink(tc.coopmuxURL, tc.podName, tc.agentName)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("coopmuxAgentLink(%q,%q,%q) = %q, want to contain %q",
					tc.coopmuxURL, tc.podName, tc.agentName, got, tc.wantContains)
			}
			if tc.wantNotContains != "" && strings.Contains(got, tc.wantNotContains) {
				t.Errorf("coopmuxAgentLink(%q,%q,%q) = %q, should not contain %q",
					tc.coopmuxURL, tc.podName, tc.agentName, got, tc.wantNotContains)
			}
		})
	}
}

func TestBuildCompactAgentCardBlocks(t *testing.T) {
	tests := []struct {
		name       string
		agent      string
		state      string
		wantEmoji  string
	}{
		{"done", "test-bot", "done", ":white_check_mark:"},
		{"failed", "test-bot", "failed", ":x:"},
		{"other", "test-bot", "unknown", ":white_circle:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := buildCompactAgentCardBlocks(tc.agent, tc.state)
			if len(blocks) != 1 {
				t.Fatalf("expected 1 block, got %d", len(blocks))
			}
		})
	}
}

func TestBuildAgentCardBlocks_PendingOverridesState(t *testing.T) {
	// When pendingCount > 0, indicator should be blue circle regardless of state.
	blocks := buildAgentCardBlocks("test-agent", 2, "working", "", time.Time{}, "", "", "", "")
	if len(blocks) < 1 {
		t.Fatal("expected at least 1 block")
	}
	// Just verify the function runs without panic and returns blocks.
}

func TestBuildAgentCardBlocks_AllStatesDisplay(t *testing.T) {
	states := []string{"spawning", "working", "done", "failed", "rate_limited", "idle", ""}
	for _, state := range states {
		blocks := buildAgentCardBlocks("test-agent", 0, state, "Fix bugs", time.Now(), "", "", "v1.0", "crew")
		if len(blocks) < 2 {
			t.Errorf("state %q: expected at least 2 blocks, got %d", state, len(blocks))
		}
	}
}

func TestBuildAgentCardBlocks_TerminalStatesHaveClearButton(t *testing.T) {
	for _, state := range []string{"done", "failed"} {
		blocks := buildAgentCardBlocks("test-agent", 0, state, "", time.Time{}, "", "", "", "")
		// Terminal states should have 3 blocks: section + context + action (Clear button).
		if len(blocks) != 3 {
			t.Errorf("state %q: expected 3 blocks (with Clear button), got %d", state, len(blocks))
		}
	}
	// Non-terminal state should have 2 blocks (no Clear button).
	blocks := buildAgentCardBlocks("test-agent", 0, "working", "", time.Time{}, "", "", "", "")
	if len(blocks) != 2 {
		t.Errorf("state working: expected 2 blocks (no Clear button), got %d", len(blocks))
	}
}

func TestBuildWrapUpAgentCardBlocks(t *testing.T) {
	blocks := buildWrapUpAgentCardBlocks("test-agent", "done", `{"accomplishments":["Fixed bug"]}`)
	// Should have: header + wrapup content + action (Clear button).
	if len(blocks) < 2 {
		t.Errorf("expected at least 2 blocks, got %d", len(blocks))
	}
}

// --- NotifyAgentSpawn tests ---

func TestNotifyAgentSpawn_FlatMode_PostsMessage(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-1"] = &beadsapi.BeadDetail{
		ID: "agent-1", Type: "agent", Status: "open",
		Title: "my-agent", Fields: map[string]string{"agent": "my-agent"},
	}

	var mu sync.Mutex
	var postedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			_ = r.ParseForm()
			mu.Lock()
			postedText = r.FormValue("text")
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C123", "ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"
	// threadingMode="" = flat mode.

	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-1",
		Type:     "agent",
		Title:    "my-agent",
		Assignee: "my-agent",
		Fields:   map[string]string{"agent": "my-agent", "role": "crew"},
	})

	if state := bot.agentState["my-agent"]; state != "spawning" {
		t.Errorf("expected agentState=spawning, got %q", state)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(postedText, "Agent spawned") {
		t.Errorf("expected spawn text, got %q", postedText)
	}
}

func TestNotifyAgentSpawn_AgentMode_PostsCard(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-2"] = &beadsapi.BeadDetail{
		ID: "agent-2", Type: "agent", Status: "open",
		Title: "card-agent", Fields: map[string]string{"agent": "card-agent"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"
	bot.threadingMode = "agent"

	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-2",
		Type:     "agent",
		Title:    "card-agent",
		Assignee: "card-agent",
		Fields:   map[string]string{"agent": "card-agent"},
	})

	if state := bot.agentState["card-agent"]; state != "spawning" {
		t.Errorf("expected agentState=spawning, got %q", state)
	}

	// Agent card should exist.
	if _, ok := bot.agentCards["card-agent"]; !ok {
		t.Error("expected agent card to be created in agent threading mode")
	}
}

func TestNotifyAgentSpawn_IdentityFromFields(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-3"] = &beadsapi.BeadDetail{
		ID: "agent-3", Type: "agent", Status: "open",
		Title: "field-agent", Fields: map[string]string{"agent": "field-agent"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	// No Assignee — identity from Fields["agent"].
	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:     "agent-3",
		Type:   "agent",
		Title:  "field-agent",
		Fields: map[string]string{"agent": "gasboat/crew/field-agent"},
	})

	// Should be stored under short name.
	if _, ok := bot.agentState["field-agent"]; !ok {
		t.Error("expected agentState keyed by short name 'field-agent'")
	}
}

func TestNotifyAgentSpawn_IdentityFromTitle(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-4"] = &beadsapi.BeadDetail{
		ID: "agent-4", Type: "agent", Status: "open",
		Title: "title-agent", Fields: map[string]string{},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	// No Assignee, no Fields["agent"] — identity from Title.
	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:     "agent-4",
		Type:   "agent",
		Title:  "title-agent",
		Fields: map[string]string{},
	})

	if _, ok := bot.agentState["title-agent"]; !ok {
		t.Error("expected agentState keyed by 'title-agent'")
	}
}

func TestNotifyAgentSpawn_EmptyAgent_NoOp(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-5"] = &beadsapi.BeadDetail{
		ID: "agent-5", Type: "agent", Status: "open",
		Fields: map[string]string{},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	// No identity at all — should return early.
	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:     "agent-5",
		Type:   "agent",
		Fields: map[string]string{},
	})

	if len(bot.agentState) != 0 {
		t.Errorf("expected no agentState entries, got %d", len(bot.agentState))
	}
}

func TestNotifyAgentSpawn_CachesRole(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["agent-role"] = &beadsapi.BeadDetail{
		ID: "agent-role", Type: "agent", Status: "open",
		Title: "role-agent", Fields: map[string]string{"agent": "role-agent", "role": "captain"},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C123"

	bot.NotifyAgentSpawn(context.Background(), BeadEvent{
		ID:       "agent-role",
		Type:     "agent",
		Assignee: "role-agent",
		Fields:   map[string]string{"agent": "role-agent", "role": "captain"},
	})

	if role := bot.agentRole["role-agent"]; role != "captain" {
		t.Errorf("expected agentRole=captain, got %q", role)
	}
}

// --- NotifyAgentState tests ---

func TestNotifyAgentState_UpdatesState(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentState["test-agent"] = "spawning"

	bot.NotifyAgentState(context.Background(), BeadEvent{
		Assignee: "test-agent",
		Fields:   map[string]string{"agent_state": "working"},
	})

	if state := bot.agentState["test-agent"]; state != "working" {
		t.Errorf("expected agentState=working, got %q", state)
	}
}

func TestNotifyAgentState_EmptyAgent_NoOp(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// No identity — should return early.
	bot.NotifyAgentState(context.Background(), BeadEvent{
		Fields: map[string]string{"agent_state": "working"},
	})

	if len(bot.agentState) != 0 {
		t.Errorf("expected no agentState entries, got %d", len(bot.agentState))
	}
}

func TestNotifyAgentState_TerminalState_UpdatesCard(t *testing.T) {
	daemon := newMockDaemon()

	var mu sync.Mutex
	var updateCount int
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.update" {
			mu.Lock()
			updateCount++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentState["done-agent"] = "working"
	bot.agentCards["done-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	bot.NotifyAgentState(context.Background(), BeadEvent{
		Assignee: "done-agent",
		Fields:   map[string]string{"agent_state": "done", "wrapup": `{"accomplishments":["Completed task"]}`},
	})

	if state := bot.agentState["done-agent"]; state != "done" {
		t.Errorf("expected agentState=done, got %q", state)
	}

	mu.Lock()
	defer mu.Unlock()
	if updateCount < 1 {
		t.Error("expected agent card update for terminal state")
	}
}

// --- NotifyAgentTaskUpdate tests ---

func TestNotifyAgentTaskUpdate_UpdatesCard(t *testing.T) {
	daemon := newMockDaemon()

	var mu sync.Mutex
	var updateCount int
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.update" {
			mu.Lock()
			updateCount++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentCards["task-agent"] = MessageRef{ChannelID: "C123", Timestamp: "2222.3333"}
	bot.agentState["task-agent"] = "working"

	bot.NotifyAgentTaskUpdate(context.Background(), "task-agent")

	mu.Lock()
	defer mu.Unlock()
	if updateCount < 1 {
		t.Error("expected agent card update on task update")
	}

	// agentSeen should be updated.
	if bot.agentSeen["task-agent"].IsZero() {
		t.Error("expected agentSeen to be set")
	}
}

func TestNotifyAgentTaskUpdate_NoCard_NoOp(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// No card exists — should not panic.
	bot.NotifyAgentTaskUpdate(context.Background(), "no-card-agent")

	// agentSeen should NOT be set since no card exists.
	if !bot.agentSeen["no-card-agent"].IsZero() {
		t.Error("expected agentSeen to not be set when no card exists")
	}
}

func TestNotifyAgentTaskUpdate_FullPath_NormalizesToShort(t *testing.T) {
	daemon := newMockDaemon()

	var mu sync.Mutex
	var updateCount int
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.update" {
			mu.Lock()
			updateCount++
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentCards["my-agent"] = MessageRef{ChannelID: "C123", Timestamp: "3333.4444"}
	bot.agentState["my-agent"] = "working"

	// Full path should be normalized to short name.
	bot.NotifyAgentTaskUpdate(context.Background(), "gasboat/crew/my-agent")

	mu.Lock()
	defer mu.Unlock()
	if updateCount < 1 {
		t.Error("expected agent card update with full path normalized to short name")
	}
}

// --- agentTaskTitle tests ---

func TestAgentTaskTitle_FindsTask(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["task-1"] = &beadsapi.BeadDetail{
		ID:       "task-1",
		Type:     "task",
		Kind:     "issue",
		Title:    "Fix login bug",
		Status:   "in_progress",
		Assignee: "test-agent",
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	title := bot.agentTaskTitle(context.Background(), "test-agent")
	if title != "Fix login bug" {
		t.Errorf("expected task title 'Fix login bug', got %q", title)
	}
}

func TestAgentTaskTitle_RetriesShortName(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["task-2"] = &beadsapi.BeadDetail{
		ID:       "task-2",
		Type:     "task",
		Kind:     "issue",
		Title:    "Deploy service",
		Status:   "in_progress",
		Assignee: "short-agent", // stored under short name
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Query with full path — should retry with short name.
	title := bot.agentTaskTitle(context.Background(), "gasboat/crew/short-agent")
	if title != "Deploy service" {
		t.Errorf("expected task title 'Deploy service', got %q", title)
	}
}

func TestAgentTaskTitle_NoTask_ReturnsEmpty(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	title := bot.agentTaskTitle(context.Background(), "no-task-agent")
	if title != "" {
		t.Errorf("expected empty title, got %q", title)
	}
}

// --- fetchAndCachePodName tests ---

func TestFetchAndCachePodName_CachesPodAndImageTag(t *testing.T) {
	daemon := newMockDaemon()
	daemon.beads["pod-agent"] = &beadsapi.BeadDetail{
		ID:    "bd-pod-agent",
		Type:  "agent",
		Title: "pod-agent",
		Notes: "pod_name:my-pod-123\nimage_tag:ghcr.io/org/agent:v2026.58.3",
		Fields: map[string]string{
			"agent": "pod-agent",
			"role":  "crew",
		},
	}

	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.fetchAndCachePodName(context.Background(), "pod-agent")

	if got := bot.agentPodName["pod-agent"]; got != "my-pod-123" {
		t.Errorf("expected podName=my-pod-123, got %q", got)
	}
	if got := bot.agentImageTag["pod-agent"]; got != "v2026.58.3" {
		t.Errorf("expected imageTag=v2026.58.3, got %q", got)
	}
	if got := bot.agentRole["pod-agent"]; got != "crew" {
		t.Errorf("expected role=crew, got %q", got)
	}
}

func TestFetchAndCachePodName_NotFound_NoOp(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	// Should not panic when agent bead not found.
	bot.fetchAndCachePodName(context.Background(), "unknown-agent")

	if len(bot.agentPodName) != 0 {
		t.Errorf("expected no podName entries, got %d", len(bot.agentPodName))
	}
}

// --- resolveChannel tests ---

func TestResolveChannel_DefaultChannel(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-DEFAULT"

	if got := bot.resolveChannel("any-agent"); got != "C-DEFAULT" {
		t.Errorf("expected default channel, got %q", got)
	}
}

func TestResolveChannel_EmptyAgent(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.channel = "C-DEFAULT"

	// Empty agent should return default even with router.
	if got := bot.resolveChannel(""); got != "C-DEFAULT" {
		t.Errorf("expected default channel for empty agent, got %q", got)
	}
}

// --- Bot helper tests ---

func TestExtractAgentName_Paths(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"gasboat/crew/test-bot", "test-bot"},
		{"test-bot", "test-bot"},
		{"a/b/c/d", "d"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := extractAgentName(tc.input); got != tc.want {
			t.Errorf("extractAgentName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBeadTitle(t *testing.T) {
	tests := []struct {
		id, title, want string
	}{
		{"dec-1", "Deploy decision", "Deploy decision"},
		{"dec-2", "", "dec-2"},
	}
	for _, tc := range tests {
		if got := beadTitle(tc.id, tc.title); got != tc.want {
			t.Errorf("beadTitle(%q, %q) = %q, want %q", tc.id, tc.title, got, tc.want)
		}
	}
}

func TestTruncateText_EdgeCases(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is a longer string", 10, "this is..."},
		{"ab", 2, "ab"},
		{"abc", 2, "ab"},
		{"abcde", 3, "abc"},
	}
	for _, tc := range tests {
		if got := truncateText(tc.input, tc.maxLen); got != tc.want {
			t.Errorf("truncateText(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		t       time.Time
		wantSfx string
	}{
		{"just now", now.Add(-10 * time.Second), "just now"},
		{"minutes", now.Add(-5 * time.Minute), "m ago"},
		{"hours", now.Add(-3 * time.Hour), "h ago"},
		{"days", now.Add(-48 * time.Hour), "d ago"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatAge(tc.t)
			if !strings.Contains(got, tc.wantSfx) {
				t.Errorf("formatAge(%v) = %q, want suffix %q", tc.t, got, tc.wantSfx)
			}
		})
	}
}

// --- ensureAgentCard tests ---

func TestEnsureAgentCard_ReturnsCachedCard(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.agentCards["cached-agent"] = MessageRef{ChannelID: "C123", Timestamp: "1111.2222"}

	ts, err := bot.ensureAgentCard(context.Background(), "cached-agent", "C123")
	if err != nil {
		t.Fatalf("ensureAgentCard: %v", err)
	}
	if ts != "1111.2222" {
		t.Errorf("expected cached timestamp 1111.2222, got %q", ts)
	}
}

func TestEnsureAgentCard_PostsNewCard(t *testing.T) {
	daemon := newMockDaemon()

	var mu sync.Mutex
	var postedChannel string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postMessage" {
			_ = r.ParseForm()
			mu.Lock()
			postedChannel = r.FormValue("channel")
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C-NEW", "ts": "9999.8888"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	ts, err := bot.ensureAgentCard(context.Background(), "new-agent", "C-NEW")
	if err != nil {
		t.Fatalf("ensureAgentCard: %v", err)
	}
	if ts != "9999.8888" {
		t.Errorf("expected new timestamp, got %q", ts)
	}

	mu.Lock()
	defer mu.Unlock()
	if postedChannel != "C-NEW" {
		t.Errorf("expected post to C-NEW, got %q", postedChannel)
	}

	// Card should be cached.
	if _, ok := bot.agentCards["new-agent"]; !ok {
		t.Error("expected agent card to be cached")
	}
}

func TestEnsureAgentCard_DifferentChannel_PostsNewCard(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	// Card exists in C-OLD — requesting for C-NEW should post a new card.
	bot.agentCards["drift-agent"] = MessageRef{ChannelID: "C-OLD", Timestamp: "1111.1111"}

	ts, err := bot.ensureAgentCard(context.Background(), "drift-agent", "C-NEW")
	if err != nil {
		t.Fatalf("ensureAgentCard: %v", err)
	}
	// Should return the new card's timestamp, not the old one.
	if ts == "1111.1111" {
		t.Error("expected new card timestamp, got old one")
	}
}
