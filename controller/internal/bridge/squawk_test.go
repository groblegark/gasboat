package bridge

import (
	"context"
	"log/slog"
	"testing"
)

func TestSquawk_HandleClosed_IgnoresNonMessageBeads(t *testing.T) {
	s := &Squawk{
		daemon: newMockDaemon(),
		logger: slog.Default(),
		seen:   make(map[string]bool),
	}

	// Task bead should be ignored.
	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-task1",
		Type:   "task",
		Labels: []string{"bug"},
	})
	s.handleClosed(context.Background(), data)
	// No panic, no action — just verify it doesn't crash.
}

func TestSquawk_HandleClosed_IgnoresMessageWithoutSquawkLabel(t *testing.T) {
	s := &Squawk{
		daemon: newMockDaemon(),
		logger: slog.Default(),
		seen:   make(map[string]bool),
	}

	// Message bead without "squawk" label should be ignored.
	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-msg1",
		Type:   "message",
		Labels: []string{"from:test-agent"},
	})
	s.handleClosed(context.Background(), data)
	// No crash, no action.
}

func TestSquawk_HandleClosed_ProcessesSquawkMessage(t *testing.T) {
	s := &Squawk{
		daemon: newMockDaemon(),
		logger: slog.Default(),
		seen:   make(map[string]bool),
		// bot is nil — Slack post will be skipped, but processing still happens.
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-squawk1",
		Type:   "message",
		Labels: []string{"squawk", "from:test-agent"},
		Fields: map[string]string{
			"source_agent": "test-agent",
			"text":         "Build complete, MR ready for review",
		},
	})
	s.handleClosed(context.Background(), data)

	// Verify dedup: same bead should be skipped on replay.
	if !s.alreadySeen("bd-squawk1") {
		t.Error("expected bead to be marked as seen after processing")
	}
}

func TestSquawk_HandleClosed_Dedup(t *testing.T) {
	s := &Squawk{
		daemon: newMockDaemon(),
		logger: slog.Default(),
		seen:   make(map[string]bool),
	}

	data := marshalSSEBeadPayload(BeadEvent{
		ID:     "bd-squawk2",
		Type:   "message",
		Labels: []string{"squawk", "from:agent-1"},
		Fields: map[string]string{
			"source_agent": "agent-1",
			"text":         "First message",
		},
	})

	// First call processes it.
	s.handleClosed(context.Background(), data)
	if !s.alreadySeen("bd-squawk2") {
		t.Error("expected bead to be seen after first call")
	}

	// Second call should be deduped (no panic, no double processing).
	s.handleClosed(context.Background(), data)
}

func TestExtractSquawkAgent(t *testing.T) {
	tests := []struct {
		name string
		bead BeadEvent
		want string
	}{
		{
			name: "from fields",
			bead: BeadEvent{
				Fields: map[string]string{"source_agent": "gasboat/crew/my-agent"},
				Labels: []string{"from:other"},
			},
			want: "my-agent",
		},
		{
			name: "from label",
			bead: BeadEvent{
				Labels: []string{"squawk", "from:label-agent"},
			},
			want: "label-agent",
		},
		{
			name: "from created_by",
			bead: BeadEvent{
				CreatedBy: "gasboat/crew/creator-agent",
			},
			want: "creator-agent",
		},
		{
			name: "empty",
			bead: BeadEvent{},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSquawkAgent(tt.bead)
			if got != tt.want {
				t.Errorf("extractSquawkAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}
