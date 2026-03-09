package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// mockDaemon is a test HTTP server that returns canned bead responses.
func mockDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	// Serve canned bead listings based on query params.
	mux.HandleFunc("/v1/beads", func(w http.ResponseWriter, r *http.Request) {
		beadType := r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/json")

		switch {
		case beadType == "config":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"beads": []map[string]any{
					{
						"id":     "cfg-1",
						"title":  "claude-settings",
						"type":   "config",
						"kind":   "config",
						"status": "open",
						"labels": []string{"global"},
						"fields": map[string]any{
							"value": `{"model":"sonnet"}`,
						},
					},
					{
						"id":     "cfg-2",
						"title":  "claude-instructions",
						"type":   "config",
						"kind":   "config",
						"status": "open",
						"labels": []string{"role:crew"},
						"fields": map[string]any{
							"value": `{"lifecycle":"persistent"}`,
						},
					},
					{
						"id":     "cfg-3",
						"title":  "claude-settings",
						"type":   "config",
						"kind":   "config",
						"status": "open",
						"labels": []string{"role:captain"},
						"fields": map[string]any{
							"value": `{"model":"opus"}`,
						},
					},
				},
				"total": 3,
			})
		case beadType == "advice":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"beads": []map[string]any{
					{
						"id":          "adv-1",
						"title":       "Use gb prime for context recovery",
						"type":        "advice",
						"kind":        "data",
						"status":      "open",
						"labels":      []string{"global"},
						"description": "Run gb prime after compaction",
					},
					{
						"id":          "adv-2",
						"title":       "Crew-specific advice",
						"type":        "advice",
						"kind":        "data",
						"status":      "open",
						"labels":      []string{"role:crew"},
						"description": "Crew agents should ...",
					},
				},
				"total": 2,
			})
		case beadType == "agent":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"beads": []map[string]any{
					{
						"id":     "agent-1",
						"title":  "test-bot",
						"type":   "agent",
						"kind":   "config",
						"status": "open",
						"labels": []string{"role:crew", "project:gasboat"},
						"fields": map[string]any{
							"agent":   "test-bot",
							"role":    "crew",
							"mode":    "crew",
							"project": "gasboat",
						},
					},
				},
				"total": 1,
			})
		case beadType == "project":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"beads": []map[string]any{
					{
						"id":     "proj-1",
						"title":  "gasboat",
						"type":   "project",
						"kind":   "config",
						"status": "open",
						"labels": []string{},
						"fields": map[string]any{
							"git_url":        "https://github.com/groblegark/gasboats.git",
							"default_branch": "main",
						},
					},
				},
				"total": 1,
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"beads": []any{}, "total": 0})
		}
	})

	return httptest.NewServer(mux)
}

func setupTestAPI(t *testing.T) (*RolesAPI, *httptest.Server) {
	t.Helper()
	daemon := mockDaemon(t)
	client, err := beadsapi.New(beadsapi.Config{HTTPAddr: daemon.URL})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() {
		client.Close()
		daemon.Close()
	})
	return NewRolesAPI(client, setupLogger("error")), daemon
}

func TestListRoles(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/roles", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Roles []roleInfo `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.Roles) < 3 {
		t.Fatalf("expected at least 3 roles (global, crew, captain), got %d", len(resp.Roles))
	}

	// Verify "global" sorts first.
	if resp.Roles[0].Name != "global" {
		t.Errorf("expected first role to be 'global', got %q", resp.Roles[0].Name)
	}

	// Verify crew role has config and advice beads.
	var crewRole *roleInfo
	for i := range resp.Roles {
		if resp.Roles[i].Name == "crew" {
			crewRole = &resp.Roles[i]
			break
		}
	}
	if crewRole == nil {
		t.Fatal("expected to find 'crew' role")
	}
	if len(crewRole.ConfigBeads) == 0 {
		t.Error("expected crew role to have config beads")
	}
	if len(crewRole.AdviceBeads) == 0 {
		t.Error("expected crew role to have advice beads")
	}
	if crewRole.AgentCount != 1 {
		t.Errorf("expected crew role to have 1 active agent, got %d", crewRole.AgentCount)
	}
}

func TestGetRole(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/roles/crew", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp roleInfo
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Name != "crew" {
		t.Errorf("expected role name 'crew', got %q", resp.Name)
	}
	if len(resp.ConfigBeads) == 0 {
		t.Error("expected crew role to have config beads")
	}
}

func TestGetRoleGlobal(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/roles/global", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp roleInfo
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Name != "global" {
		t.Errorf("expected role name 'global', got %q", resp.Name)
	}
	if len(resp.ConfigBeads) == 0 {
		t.Error("expected global role to have config beads")
	}
	if len(resp.AdviceBeads) == 0 {
		t.Error("expected global role to have advice beads")
	}
	// Global role should NOT include agents that have a specific role.
	if resp.AgentCount != 0 {
		t.Errorf("expected global role to have 0 agents (agents have role=crew), got %d", resp.AgentCount)
	}
}

func TestGetRoleNonexistent(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/roles/nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp roleInfo
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.AgentCount != 0 {
		t.Errorf("expected 0 agents for nonexistent role, got %d", resp.AgentCount)
	}
}

func TestListConfigBeads(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/config-beads", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		ConfigBeads []configBead `json:"config_beads"`
		Total       int          `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.ConfigBeads) != 3 {
		t.Errorf("expected 3 config beads, got %d", len(resp.ConfigBeads))
	}
}

func TestListAdvice(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/advice", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		AdviceBeads []adviceBead `json:"advice_beads"`
		Total       int          `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(resp.AdviceBeads) != 2 {
		t.Errorf("expected 2 advice beads, got %d", len(resp.AdviceBeads))
	}
}

func TestListProjects(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/projects", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Projects map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if _, ok := resp.Projects["gasboat"]; !ok {
		t.Error("expected 'gasboat' project in response")
	}
}

func TestHealthz(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","version":"test"}`))
	})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestConfigBeadLabelFilter(t *testing.T) {
	api, _ := setupTestAPI(t)
	mux := http.NewServeMux()
	api.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/config-beads?labels=role:crew", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
