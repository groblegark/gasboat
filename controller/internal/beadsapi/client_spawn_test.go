package beadsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- SpawnAgent tests ---

func TestSpawnAgent_SendsCorrectRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "bd-agent-42"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	id, err := c.SpawnAgent(context.Background(), "my-bot", "gasboat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if id != "bd-agent-42" {
		t.Errorf("expected id bd-agent-42, got %s", id)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/v1/beads" {
		t.Errorf("expected /v1/beads, got %s", gotPath)
	}

	var beadType, beadTitle string
	_ = json.Unmarshal(gotBody["type"], &beadType)
	_ = json.Unmarshal(gotBody["title"], &beadTitle)
	if beadType != "agent" {
		t.Errorf("expected type=agent, got %s", beadType)
	}
	if beadTitle != "my-bot" {
		t.Errorf("expected title=my-bot, got %s", beadTitle)
	}

	var fields map[string]string
	_ = json.Unmarshal(gotBody["fields"], &fields)
	if fields["agent"] != "my-bot" {
		t.Errorf("expected fields.agent=my-bot, got %s", fields["agent"])
	}
	if fields["project"] != "gasboat" {
		t.Errorf("expected fields.project=gasboat, got %s", fields["project"])
	}
	if fields["mode"] != "crew" {
		t.Errorf("expected fields.mode=crew, got %s", fields["mode"])
	}
	if fields["role"] != "crew" {
		t.Errorf("expected fields.role=crew, got %s", fields["role"])
	}
}

func TestSpawnAgent_PropagatesCreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.SpawnAgent(context.Background(), "bad-bot", "gasboat")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
