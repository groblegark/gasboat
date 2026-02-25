package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"log/slog"

	"github.com/slack-go/slack"
)

// newTestBot creates a Bot wired to a fake Slack API server for unit tests.
// The server responds OK to all requests (PostEphemeral, etc.).
func newTestBot(daemon BeadClient, slackSrv *httptest.Server) *Bot {
	api := slack.New("xoxb-test", slack.OptionAPIURL(slackSrv.URL+"/"))
	return &Bot{
		api:          api,
		daemon:       daemon,
		logger:       slog.Default(),
		messages:     make(map[string]MessageRef),
		agentCards:   make(map[string]MessageRef),
		agentPending: make(map[string]int),
	}
}

// newFakeSlackServer returns an httptest.Server that accepts any Slack API call
// and returns a generic OK response.
func newFakeSlackServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
}

func TestHandleSpawnCommand_SpawnsAgentWithProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "my-bot gasboat",
		ChannelID: "C123",
		UserID:    "U456",
	})

	// SpawnAgent should have created an agent bead.
	if len(daemon.beads) != 1 {
		t.Fatalf("expected 1 bead created, got %d", len(daemon.beads))
	}
	for _, b := range daemon.beads {
		if b.Type != "agent" {
			t.Errorf("expected type=agent, got %s", b.Type)
		}
		if b.Title != "my-bot" {
			t.Errorf("expected title=my-bot, got %s", b.Title)
		}
		if b.Fields["project"] != "gasboat" {
			t.Errorf("expected project=gasboat, got %s", b.Fields["project"])
		}
	}
}

func TestHandleSpawnCommand_SpawnsAgentNoProject(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "my-bot",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 1 {
		t.Fatalf("expected 1 bead created, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_EmptyArgs_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for empty args, got %d", len(daemon.beads))
	}
}

func TestHandleSpawnCommand_InvalidAgentName_NoBeadCreated(t *testing.T) {
	daemon := newMockDaemon()
	slackSrv := newFakeSlackServer(t)
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)

	bot.handleSpawnCommand(context.Background(), slack.SlashCommand{
		Command:   "/spawn",
		Text:      "My_Bot!",
		ChannelID: "C123",
		UserID:    "U456",
	})

	if len(daemon.beads) != 0 {
		t.Errorf("expected no bead created for invalid name, got %d", len(daemon.beads))
	}
}

func TestIsValidAgentName(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  bool
	}{
		{"lowercase", "my-bot", true},
		{"digits", "bot2", true},
		{"hyphens", "a-b-c", true},
		{"empty", "", false},
		{"uppercase", "MyBot", false},
		{"underscore", "my_bot", false},
		{"space", "my bot", false},
		{"special", "bot!", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidAgentName(tc.input); got != tc.want {
				t.Errorf("isValidAgentName(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
