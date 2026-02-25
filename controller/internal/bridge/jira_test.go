package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// mockJiraDaemon implements JiraBeadClient for testing.
type mockJiraDaemon struct {
	mu     sync.Mutex
	beads  map[string]*beadsapi.BeadDetail
	nextID int
}

func newMockJiraDaemon() *mockJiraDaemon {
	return &mockJiraDaemon{
		beads: make(map[string]*beadsapi.BeadDetail),
	}
}

func (m *mockJiraDaemon) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("bd-task-%d", m.nextID)
	fields := beadsapi.ParseFieldsJSON(req.Fields)
	fields["_priority"] = fmt.Sprintf("%d", req.Priority)
	m.beads[id] = &beadsapi.BeadDetail{
		ID:          id,
		Title:       req.Title,
		Type:        req.Type,
		Labels:      req.Labels,
		Description: req.Description,
		CreatedBy:   req.CreatedBy,
		Fields:      fields,
	}
	return id, nil
}

func (m *mockJiraDaemon) ListTaskBeads(_ context.Context) ([]*beadsapi.BeadDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*beadsapi.BeadDetail
	for _, b := range m.beads {
		if b.Type == "task" {
			result = append(result, b)
		}
	}
	return result, nil
}

func (m *mockJiraDaemon) getBeads() map[string]*beadsapi.BeadDetail {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*beadsapi.BeadDetail, len(m.beads))
	for k, v := range m.beads {
		out[k] = v
	}
	return out
}

// newTestJiraClient creates a JiraClient pointing at a test server.
func newTestJiraClient(url string) *JiraClient {
	return NewJiraClient(JiraClientConfig{
		BaseURL: url, Email: "test@example.com", APIToken: "tok", Logger: slog.Default(),
	})
}

func TestJiraPoller_CreateBead(t *testing.T) {
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-7001", "id": "10001",
				"fields": map[string]any{
					"summary": "Error alert after uploading file",
					"description": map[string]any{"version": 1, "type": "doc", "content": []any{
						map[string]any{"type": "paragraph", "content": []any{
							map[string]any{"type": "text", "text": "Steps to reproduce the error."},
						}},
					}},
					"status": map[string]string{"name": "To Do"}, "issuetype": map[string]string{"name": "Bug"},
					"priority": map[string]string{"name": "High"},
					"reporter": map[string]string{"displayName": "Jane Doe", "accountId": "abc123"},
					"labels":   []string{"frontend", "urgent"},
					"parent":   map[string]any{"key": "PE-5000", "fields": map[string]string{"summary": "Upload Epic"}},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"PE"}, Statuses: []string{"To Do"}, IssueTypes: []string{"Bug"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())

	beads := daemon.getBeads()
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead, got %d", len(beads))
	}
	var bead *beadsapi.BeadDetail
	for _, b := range beads {
		bead = b
	}
	if bead.Title != "[PE-7001] Error alert after uploading file" {
		t.Errorf("unexpected title: %s", bead.Title)
	}
	if bead.Type != "task" {
		t.Errorf("expected type=task, got %s", bead.Type)
	}
	want := map[string]bool{"source:jira": true, "jira:PE-7001": true, "project:pe": true, "jira-label:frontend": true, "jira-label:urgent": true}
	for _, l := range bead.Labels {
		delete(want, l)
	}
	if len(want) > 0 {
		t.Errorf("missing labels: %v", want)
	}
	if bead.Fields["jira_key"] != "PE-7001" {
		t.Errorf("jira_key=%s", bead.Fields["jira_key"])
	}
	if bead.Fields["jira_type"] != "Bug" {
		t.Errorf("jira_type=%s", bead.Fields["jira_type"])
	}
	if bead.Fields["jira_epic"] != "PE-5000" {
		t.Errorf("jira_epic=%s", bead.Fields["jira_epic"])
	}
	if bead.Fields["_priority"] != "1" {
		t.Errorf("priority=%s, want 1", bead.Fields["_priority"])
	}
	if bead.CreatedBy != "jira-bridge" {
		t.Errorf("created_by=%s", bead.CreatedBy)
	}
	if bead.Description != "Steps to reproduce the error." {
		t.Errorf("description=%q", bead.Description)
	}
}

func TestJiraPoller_Dedup(t *testing.T) {
	callCount := 0
	jiraServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/api/3/search/jql" {
			http.NotFound(w, r)
			return
		}
		callCount++
		resp := map[string]any{
			"issues": []map[string]any{{
				"key": "PE-100", "id": "100",
				"fields": map[string]any{
					"summary": "Dup test", "status": map[string]string{"name": "To Do"},
					"issuetype": map[string]string{"name": "Task"}, "priority": map[string]string{"name": "Medium"},
				},
			}},
			"total": 1,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jiraServer.Close()

	daemon := newMockJiraDaemon()
	poller := NewJiraPoller(newTestJiraClient(jiraServer.URL), daemon, JiraPollerConfig{
		Projects: []string{"PE"}, Statuses: []string{"To Do"}, IssueTypes: []string{"Task"}, Logger: slog.Default(),
	})
	poller.poll(context.Background())
	poller.poll(context.Background())

	if len(daemon.getBeads()) != 1 {
		t.Fatalf("expected 1 bead after 2 polls (dedup), got %d", len(daemon.getBeads()))
	}
	if callCount != 2 {
		t.Errorf("expected 2 JIRA API calls, got %d", callCount)
	}
}

func TestJiraPoller_CatchUp(t *testing.T) {
	daemon := newMockJiraDaemon()
	daemon.mu.Lock()
	daemon.beads["existing-1"] = &beadsapi.BeadDetail{
		ID: "existing-1", Type: "task", Labels: []string{"source:jira", "jira:PE-500"},
		Fields: map[string]string{"jira_key": "PE-500"},
	}
	daemon.beads["non-jira"] = &beadsapi.BeadDetail{
		ID: "non-jira", Type: "task", Labels: []string{"source:manual"}, Fields: map[string]string{},
	}
	daemon.mu.Unlock()

	poller := NewJiraPoller(nil, daemon, JiraPollerConfig{Logger: slog.Default()})
	poller.CatchUp(context.Background())

	if !poller.IsTracked("PE-500") {
		t.Error("expected PE-500 to be tracked")
	}
	if poller.IsTracked("non-jira") {
		t.Error("non-JIRA bead should not be tracked")
	}
	if poller.TrackedCount() != 1 {
		t.Errorf("expected 1 tracked, got %d", poller.TrackedCount())
	}
}

func TestMapJiraPriority(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"Highest", 0}, {"Critical", 0}, {"Blocker", 0}, {"High", 1},
		{"Medium", 2}, {"Low", 3}, {"Lowest", 3}, {"Trivial", 3}, {"unknown", 2}, {"", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapJiraPriority(tt.name); got != tt.expected {
				t.Errorf("MapJiraPriority(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}

func TestJiraKeyFromBead(t *testing.T) {
	tests := []struct {
		name     string
		bead     BeadEvent
		expected string
	}{
		{"from fields", BeadEvent{Fields: map[string]string{"jira_key": "PE-123"}}, "PE-123"},
		{"from labels", BeadEvent{Labels: []string{"source:jira", "jira:DEVOPS-42"}, Fields: map[string]string{}}, "DEVOPS-42"},
		{"not jira", BeadEvent{Labels: []string{"source:manual"}, Fields: map[string]string{}}, ""},
		{"jira-label no match", BeadEvent{Labels: []string{"jira-label:frontend"}, Fields: map[string]string{}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jiraKeyFromBead(tt.bead); got != tt.expected {
				t.Errorf("jiraKeyFromBead() = %q, want %q", got, tt.expected)
			}
		})
	}
}
