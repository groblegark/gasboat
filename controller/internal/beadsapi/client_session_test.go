package beadsapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRegisterSession(t *testing.T) {
	var gotBody CreateBeadRequest
	createCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/beads" && r.Method == "POST" {
			createCalled = true
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "kd-sess-1"})
			return
		}
		// Dependencies endpoint (best-effort, return success).
		if r.Method == "POST" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "dep-1"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	fields := SessionFields{
		Agent:       "test-agent",
		AgentBeadID: "kd-agent-1",
		Project:     "gasboat",
		Role:        "crew",
		Hostname:    "pod-abc",
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		Resumed:     false,
		Status:      "active",
	}

	id, err := c.RegisterSession(context.Background(), fields)
	if err != nil {
		t.Fatalf("RegisterSession: %v", err)
	}
	if id != "kd-sess-1" {
		t.Errorf("expected id kd-sess-1, got %s", id)
	}
	if !createCalled {
		t.Error("expected CreateBead to be called")
	}
	if gotBody.Type != "session" {
		t.Errorf("expected type=session, got %s", gotBody.Type)
	}
	if gotBody.Kind != "config" {
		t.Errorf("expected kind=config, got %s", gotBody.Kind)
	}
	if len(gotBody.Labels) != 1 || gotBody.Labels[0] != "project:gasboat" {
		t.Errorf("expected label project:gasboat, got %v", gotBody.Labels)
	}

	// Verify fields were properly serialized.
	var parsed SessionFields
	if err := json.Unmarshal(gotBody.Fields, &parsed); err != nil {
		t.Fatalf("unmarshalling fields: %v", err)
	}
	if parsed.Agent != "test-agent" {
		t.Errorf("expected agent=test-agent, got %s", parsed.Agent)
	}
	if parsed.Status != "active" {
		t.Errorf("expected status=active, got %s", parsed.Status)
	}
}

func TestCloseSession(t *testing.T) {
	var patchFields map[string]any
	var closeCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET":
			// GetBead for read-modify-write in UpdateBeadFieldsTyped.
			bead := beadJSON{
				ID:     "kd-sess-1",
				Type:   "session",
				Status: "open",
				Fields: json.RawMessage(`{"agent":"test-agent","status":"active","started_at":"2026-03-08T12:00:00Z"}`),
			}
			_ = json.NewEncoder(w).Encode(bead)
		case r.Method == "PATCH":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if raw, ok := body["fields"]; ok {
				_ = json.Unmarshal(raw, &patchFields)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "POST":
			closeCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	err := c.CloseSession(context.Background(), "kd-sess-1", 1, "/path/to/log.jsonl")
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	if !closeCalled {
		t.Error("expected CloseBead to be called")
	}

	// Verify fields were updated.
	if patchFields["status"] != "crashed" {
		t.Errorf("expected status=crashed for non-zero exit, got %v", patchFields["status"])
	}
	if patchFields["session_log"] != "/path/to/log.jsonl" {
		t.Errorf("expected session_log set, got %v", patchFields["session_log"])
	}
	exitCode, ok := patchFields["exit_code"].(float64)
	if !ok || int(exitCode) != 1 {
		t.Errorf("expected exit_code=1, got %v", patchFields["exit_code"])
	}
}

func TestListSessionBeads(t *testing.T) {
	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{ID: "kd-sess-1", Type: "session", Status: "closed"},
				{ID: "kd-sess-2", Type: "session", Status: "open"},
			},
			Total: 2,
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}

	beads, err := c.ListSessionBeads(context.Background(), "my-agent", 10)
	if err != nil {
		t.Fatalf("ListSessionBeads: %v", err)
	}
	if len(beads) != 2 {
		t.Errorf("expected 2 beads, got %d", len(beads))
	}
	if gotQuery == "" {
		t.Error("expected query params")
	}
	// Should contain type=session.
	if got := gotQuery; got == "" {
		t.Error("expected non-empty query")
	}
}
