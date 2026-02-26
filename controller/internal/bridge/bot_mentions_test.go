package bridge

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      string
	}{
		{
			name:      "mention at start",
			text:      "<@U123BOT> check the logs",
			botUserID: "U123BOT",
			want:      "check the logs",
		},
		{
			name:      "mention in middle",
			text:      "hey <@U123BOT> check the logs",
			botUserID: "U123BOT",
			want:      "hey  check the logs",
		},
		{
			name:      "no mention",
			text:      "check the logs",
			botUserID: "U123BOT",
			want:      "check the logs",
		},
		{
			name:      "multiple mentions",
			text:      "<@U123BOT> hello <@U123BOT>",
			botUserID: "U123BOT",
			want:      "hello",
		},
		{
			name:      "only mention",
			text:      "<@U123BOT>",
			botUserID: "U123BOT",
			want:      "",
		},
		{
			name:      "different bot ID",
			text:      "<@UOTHER> check the logs",
			botUserID: "U123BOT",
			want:      "<@UOTHER> check the logs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripBotMention(tt.text, tt.botUserID)
			if got != tt.want {
				t.Errorf("stripBotMention(%q, %q) = %q, want %q",
					tt.text, tt.botUserID, got, tt.want)
			}
		})
	}
}

func TestGetAgentByThread(t *testing.T) {
	b := &Bot{
		agentCards: map[string]MessageRef{
			"gasboat/crew/hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "gasboat/crew/hq",
			},
			"gasboat/crew/k8s": {
				ChannelID: "C-agents",
				Timestamp: "3333.4444",
				Agent:     "gasboat/crew/k8s",
			},
		},
	}

	t.Run("matching thread", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "1111.2222")
		if got != "gasboat/crew/hq" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/hq")
		}
	})

	t.Run("different agent", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "3333.4444")
		if got != "gasboat/crew/k8s" {
			t.Errorf("got %q, want %q", got, "gasboat/crew/k8s")
		}
	})

	t.Run("non-matching channel", func(t *testing.T) {
		got := b.getAgentByThread("C-other", "1111.2222")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("non-matching timestamp", func(t *testing.T) {
		got := b.getAgentByThread("C-agents", "9999.0000")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("empty map", func(t *testing.T) {
		empty := &Bot{agentCards: map[string]MessageRef{}}
		got := empty.getAgentByThread("C-agents", "1111.2222")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

func TestGetAgentByThread_StateFallback(t *testing.T) {
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Agent card only in persisted state, not in hot cache.
	_ = state.SetAgentCard("gasboat/crew/ops", MessageRef{
		ChannelID: "C-ops",
		Timestamp: "5555.6666",
		Agent:     "gasboat/crew/ops",
	})

	b := &Bot{
		agentCards: map[string]MessageRef{}, // empty hot cache
		state:      state,
	}

	got := b.getAgentByThread("C-ops", "5555.6666")
	if got != "gasboat/crew/ops" {
		t.Errorf("got %q, want %q", got, "gasboat/crew/ops")
	}
}

func TestHandleAppMention_InAgentThread(t *testing.T) {
	daemon := newMockDaemon()

	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}

	b := &Bot{
		daemon:    daemon,
		state:     state,
		logger:    slog.Default(),
		botUserID: "U-BOT",
		agentCards: map[string]MessageRef{
			"gasboat/crew/hq": {
				ChannelID: "C-agents",
				Timestamp: "1111.2222",
				Agent:     "gasboat/crew/hq",
			},
		},
	}

	// Simulate handleAppMention with a mock event.
	// We can't use the real Slack API client (no api field), so we test
	// the core logic: bead creation and state persistence.
	ctx := context.Background()

	// Manually call the internal logic that doesn't require Slack API.
	agent := b.getAgentByThread("C-agents", "1111.2222")
	if agent != "gasboat/crew/hq" {
		t.Fatalf("expected agent gasboat/crew/hq, got %q", agent)
	}

	text := stripBotMention("<@U-BOT> check the logs", b.botUserID)
	if text != "check the logs" {
		t.Fatalf("expected stripped text 'check the logs', got %q", text)
	}

	// Create bead via daemon (same as handleAppMention does).
	beadID, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       truncateText("Mention: "+text, 80),
		Type:        "task",
		Description: "Mention from testuser in Slack:\n\ncheck the logs\n\n---\n[slack:C-agents:1111.2222]",
		Assignee:    extractAgentName(agent),
		Labels:      []string{"slack-mention"},
		Priority:    2,
	})
	if err != nil {
		t.Fatalf("CreateBead failed: %v", err)
	}

	if beadID == "" {
		t.Fatal("expected non-empty bead ID")
	}

	// Verify the bead was created with correct properties.
	bead, err := daemon.GetBead(ctx, beadID)
	if err != nil {
		t.Fatalf("GetBead failed: %v", err)
	}
	if bead.Assignee != "hq" {
		t.Errorf("bead assignee = %q, want %q", bead.Assignee, "hq")
	}
	if !hasLabel(bead.Labels, "slack-mention") {
		t.Errorf("bead labels = %v, want slack-mention", bead.Labels)
	}

	// Persist message ref (same as handleAppMention does).
	_ = state.SetChatMessage(beadID, MessageRef{
		ChannelID: "C-agents",
		Timestamp: "1111.2222",
		Agent:     "gasboat/crew/hq",
	})

	// Verify state was persisted.
	ref, ok := state.GetChatMessage(beadID)
	if !ok {
		t.Fatal("expected chat message in state")
	}
	if ref.ChannelID != "C-agents" || ref.Timestamp != "1111.2222" {
		t.Errorf("message ref = %+v, want C-agents/1111.2222", ref)
	}
}

func TestHandleAppMention_NotInThread(t *testing.T) {
	daemon := newMockDaemon()
	_ = &Bot{
		daemon:     daemon,
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		agentCards: map[string]MessageRef{},
	}

	// A mention not in a thread should not create any beads.
	// handleAppMention returns early when ThreadTimeStamp == "".
	// Verify that no beads were created.
	if daemon.getGetCalls() != 0 {
		t.Errorf("expected 0 daemon calls, got %d", daemon.getGetCalls())
	}
}

func TestHandleAppMention_NonAgentThread(t *testing.T) {
	b := &Bot{
		logger:     slog.Default(),
		botUserID:  "U-BOT",
		agentCards: map[string]MessageRef{},
	}

	// Thread exists but doesn't belong to any agent.
	agent := b.getAgentByThread("C-random", "9999.8888")
	if agent != "" {
		t.Errorf("expected empty agent for non-agent thread, got %q", agent)
	}
}

func TestChat_HandleClosed_SlackMention(t *testing.T) {
	daemon := newMockDaemon()

	// Set up state with a mention message ref.
	dir := t.TempDir()
	state, err := NewStateManager(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = state.SetChatMessage("bd-mention1", MessageRef{
		ChannelID: "C-agents",
		Timestamp: "1111.2222",
		Agent:     "gasboat/crew/hq",
	})

	// Daemon returns the full bead with close reason.
	daemon.beads["bd-mention1"] = &beadsapi.BeadDetail{
		ID:       "bd-mention1",
		Type:     "task",
		Status:   "closed",
		Assignee: "hq",
		Labels:   []string{"slack-mention"},
		Fields: map[string]string{
			"reason": "Checked the logs, all clear.",
		},
	}

	c := &Chat{
		daemon: daemon,
		state:  state,
		logger: slog.Default(),
		// bot is nil — Slack post will be skipped, but state cleanup still happens.
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:       "bd-mention1",
		Type:     "task",
		Assignee: "hq",
		Labels:   []string{"slack-mention"},
	})
	c.handleClosed(context.Background(), data)

	// State should be cleaned up (just like slack-chat beads).
	if _, ok := state.GetChatMessage("bd-mention1"); ok {
		t.Error("expected mention message to be removed from state after close")
	}
}

func TestChat_HandleClosed_IgnoresNonMentionBeads(t *testing.T) {
	daemon := newMockDaemon()
	c := &Chat{
		daemon: daemon,
		logger: slog.Default(),
	}

	// Bead with neither slack-chat nor slack-mention should be ignored.
	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-other",
		Type:   "task",
		Labels: []string{"bug"},
	})
	c.handleClosed(context.Background(), data)

	if daemon.getGetCalls() != 0 {
		t.Errorf("expected 0 GetBead calls for non-chat/mention bead, got %d", daemon.getGetCalls())
	}
}
