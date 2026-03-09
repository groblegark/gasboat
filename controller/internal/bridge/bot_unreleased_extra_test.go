package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/slack-go/slack"
)

func TestHandleUnreleasedCommand_NoGitHub(t *testing.T) {
	daemon := newMockDaemon()

	// Capture the ephemeral message posted.
	var postedText string
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postEphemeral" {
			_ = r.ParseForm()
			postedText = r.FormValue("text")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	// github is nil by default in newTestBot.

	bot.handleUnreleasedCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	})

	if postedText == "" {
		t.Fatal("expected ephemeral message to be posted")
	}
	if postedText != ":x: GitHub client not configured (GITHUB_TOKEN not set)" {
		t.Errorf("unexpected message: %s", postedText)
	}
}

func TestHandleUnreleasedCommand_WithRepos(t *testing.T) {
	daemon := newMockDaemon()

	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/myrepo/tags":
			writeJSON(w, []ghTag{{Name: "2026.60.1"}})
		case "/repos/org/myrepo/compare/2026.60.1...main":
			writeJSON(w, ghCompare{
				AheadBy: 1,
				Commits: []struct {
					SHA    string `json:"sha"`
					Commit struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					} `json:"commit"`
				}{
					{SHA: "abc1234567890", Commit: struct {
						Message string `json:"message"`
						Author  struct {
							Name string `json:"name"`
						} `json:"author"`
					}{Message: "fix: something", Author: struct {
						Name string `json:"name"`
					}{Name: "Dev"}}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghSrv.Close()

	var ephemeralPosted bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postEphemeral" {
			ephemeralPosted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.github = newTestGitHubClient(ghSrv.URL, "")
	bot.repos = []RepoRef{{Owner: "org", Repo: "myrepo"}}
	bot.version = "test-v1"

	bot.handleUnreleasedCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	})

	if !ephemeralPosted {
		t.Error("expected ephemeral message to be posted")
	}
}

func TestHandleUnreleasedCommand_UpToDate(t *testing.T) {
	daemon := newMockDaemon()

	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/tags":
			writeJSON(w, []ghTag{{Name: "2026.61.1"}})
		case "/repos/org/repo/compare/2026.61.1...main":
			writeJSON(w, ghCompare{AheadBy: 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ghSrv.Close()

	var ephemeralPosted bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postEphemeral" {
			ephemeralPosted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.github = newTestGitHubClient(ghSrv.URL, "")
	bot.repos = []RepoRef{{Owner: "org", Repo: "repo"}}
	bot.version = "v1"

	bot.handleUnreleasedCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	})

	if !ephemeralPosted {
		t.Error("expected ephemeral message to be posted")
	}
}

func TestHandleUnreleasedCommand_WithControllerVersion(t *testing.T) {
	daemon := newMockDaemon()

	ctrlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			writeJSON(w, controllerVersionInfo{
				Version:    "v3.0.0",
				Commit:     "deadbeef12345678",
				AgentImage: "agent:latest",
				Namespace:  "gasboat",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ctrlSrv.Close()

	ghSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer ghSrv.Close()

	var ephemeralPosted bool
	slackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat.postEphemeral" {
			ephemeralPosted = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "message_ts": "1234.5678"})
	}))
	defer slackSrv.Close()

	bot := newTestBot(daemon, slackSrv)
	bot.github = newTestGitHubClient(ghSrv.URL, "")
	bot.controllerURL = ctrlSrv.URL
	bot.version = "v1"

	bot.handleUnreleasedCommand(context.Background(), slack.SlashCommand{
		ChannelID: "C123",
		UserID:    "U456",
	})

	if !ephemeralPosted {
		t.Error("expected ephemeral message to be posted")
	}
}
