package beadsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Config / New tests ---

func TestNew_AutoPrependsHTTPScheme(t *testing.T) {
	c, err := New(Config{HTTPAddr: "localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("expected http://localhost:8080, got %s", c.baseURL)
	}
}

func TestNew_DoesNotDoublePrependHTTP(t *testing.T) {
	c, err := New(Config{HTTPAddr: "http://already-has-scheme:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://already-has-scheme:8080" {
		t.Errorf("expected http://already-has-scheme:8080, got %s", c.baseURL)
	}
}

func TestNew_DoesNotDoublePrependHTTPS(t *testing.T) {
	c, err := New(Config{HTTPAddr: "https://secure:443"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "https://secure:443" {
		t.Errorf("expected https://secure:443, got %s", c.baseURL)
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c, err := New(Config{HTTPAddr: "http://host:8080/"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.baseURL != "http://host:8080" {
		t.Errorf("expected trailing slash removed, got %s", c.baseURL)
	}
}

func TestNew_EmptyAddrReturnsError(t *testing.T) {
	_, err := New(Config{HTTPAddr: ""})
	if err == nil {
		t.Fatal("expected error for empty HTTPAddr")
	}
	if !strings.Contains(err.Error(), "HTTPAddr is required") {
		t.Errorf("expected 'HTTPAddr is required' error, got: %v", err)
	}
}

func TestClose_NoOp(t *testing.T) {
	c, _ := New(Config{HTTPAddr: "localhost:1"})
	if err := c.Close(); err != nil {
		t.Errorf("Close should be no-op, got error: %v", err)
	}
}

// --- ListAgentBeads tests ---

func TestListAgentBeads_QueryParams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The query should include type=agent and all active statuses.
	if !strings.Contains(gotPath, "type=agent") {
		t.Errorf("expected type=agent in query, got %s", gotPath)
	}
	// activeStatuses are joined with comma via url.Values.Set.
	for _, s := range activeStatuses {
		if !strings.Contains(gotPath, s) {
			t.Errorf("expected status %q in query, got %s", s, gotPath)
		}
	}
}

func TestListAgentBeads_ParsesBeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "crew-town-crew-hq",
					Title:  "Agent: hq",
					Type:   "agent",
					Status: "open",
					Notes:  "coop_url: http://coop:9090\npod_name: agent-hq-0",
					Fields: json.RawMessage(`{"project":"town","mode":"crew","role":"crew","agent":"hq"}`),
				},
				{
					ID:     "crew-gasboat-crew-k8s",
					Title:  "Agent: k8s",
					Type:   "agent",
					Status: "in_progress",
					Notes:  "",
					Fields: json.RawMessage(`{"project":"gasboat","role":"ops","agent":"k8s"}`),
				},
			},
			Total: 2,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	beads, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(beads) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(beads))
	}

	// First bead.
	b0 := beads[0]
	if b0.ID != "crew-town-crew-hq" {
		t.Errorf("expected ID crew-town-crew-hq, got %s", b0.ID)
	}
	if b0.Project != "town" {
		t.Errorf("expected project town, got %s", b0.Project)
	}
	if b0.Mode != "crew" {
		t.Errorf("expected mode crew, got %s", b0.Mode)
	}
	if b0.Role != "crew" {
		t.Errorf("expected role crew, got %s", b0.Role)
	}
	if b0.AgentName != "hq" {
		t.Errorf("expected agent name hq, got %s", b0.AgentName)
	}
	if b0.Metadata["coop_url"] != "http://coop:9090" {
		t.Errorf("expected coop_url metadata, got %v", b0.Metadata)
	}
	if b0.Metadata["pod_name"] != "agent-hq-0" {
		t.Errorf("expected pod_name metadata, got %v", b0.Metadata)
	}

	// Second bead -- mode defaults to "crew" when empty.
	b1 := beads[1]
	if b1.Mode != "crew" {
		t.Errorf("expected default mode crew, got %s", b1.Mode)
	}
	if b1.Role != "ops" {
		t.Errorf("expected role ops, got %s", b1.Role)
	}
}

func TestListAgentBeads_SkipsBeadsMissingRoleOrAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "missing-role",
					Fields: json.RawMessage(`{"project":"x","agent":"y"}`),
				},
				{
					ID:     "missing-agent",
					Fields: json.RawMessage(`{"project":"x","role":"y"}`),
				},
				{
					ID:     "has-both",
					Fields: json.RawMessage(`{"role":"crew","agent":"hq"}`),
				},
			},
			Total: 3,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	beads, err := c.ListAgentBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("expected 1 bead (missing role/agent should be skipped), got %d", len(beads))
	}
	if beads[0].ID != "has-both" {
		t.Errorf("expected has-both, got %s", beads[0].ID)
	}
}

// --- ListProjectBeads tests ---

func TestListProjectBeads_QueryParams(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		json.NewEncoder(w).Encode(listBeadsResponse{Beads: nil, Total: 0})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(gotPath, "type=project") {
		t.Errorf("expected type=project in query, got %s", gotPath)
	}
}

func TestListProjectBeads_ParsesProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "proj-beads",
					Title:  "Project: beads",
					Type:   "project",
					Status: "open",
					Fields: json.RawMessage(`{"prefix":"bd","git_url":"https://github.com/org/beads","default_branch":"main","image":"ghcr.io/org/beads:latest","storage_class":"gp3"}`),
				},
				{
					ID:     "proj-gasboat",
					Title:  "gasboat",
					Type:   "project",
					Status: "open",
					Fields: json.RawMessage(`{"prefix":"kd","git_url":"https://github.com/org/gasboat"}`),
				},
			},
			Total: 2,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}

	// First project -- "Project: " prefix should be stripped.
	p1, ok := projects["beads"]
	if !ok {
		t.Fatal("expected project 'beads' in map")
	}
	if p1.Name != "beads" {
		t.Errorf("expected name beads, got %s", p1.Name)
	}
	if p1.Prefix != "bd" {
		t.Errorf("expected prefix bd, got %s", p1.Prefix)
	}
	if p1.GitURL != "https://github.com/org/beads" {
		t.Errorf("expected git_url, got %s", p1.GitURL)
	}
	if p1.DefaultBranch != "main" {
		t.Errorf("expected default_branch main, got %s", p1.DefaultBranch)
	}
	if p1.Image != "ghcr.io/org/beads:latest" {
		t.Errorf("expected image, got %s", p1.Image)
	}
	if p1.StorageClass != "gp3" {
		t.Errorf("expected storage_class gp3, got %s", p1.StorageClass)
	}

	// Second project -- title without "Project: " prefix.
	p2, ok := projects["gasboat"]
	if !ok {
		t.Fatal("expected project 'gasboat' in map")
	}
	if p2.Name != "gasboat" {
		t.Errorf("expected name gasboat, got %s", p2.Name)
	}
	if p2.Prefix != "kd" {
		t.Errorf("expected prefix kd, got %s", p2.Prefix)
	}
	// Optional fields should be empty string when missing.
	if p2.DefaultBranch != "" {
		t.Errorf("expected empty default_branch, got %s", p2.DefaultBranch)
	}
}

func TestListProjectBeads_SkipsEmptyTitle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := listBeadsResponse{
			Beads: []beadJSON{
				{
					ID:     "empty-title",
					Title:  "",
					Fields: json.RawMessage(`{"prefix":"x"}`),
				},
			},
			Total: 1,
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	projects, err := c.ListProjectBeads(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0 projects for empty title, got %d", len(projects))
	}
}

// --- GetBead tests ---

func TestGetBead_ParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/beads/bd-abc123" {
			t.Errorf("expected path /api/v1/beads/bd-abc123, got %s", r.URL.Path)
		}
		bead := beadJSON{
			ID:     "bd-abc123",
			Title:  "Test bead",
			Type:   "issue",
			Status: "open",
			Labels: []string{"p1", "bug"},
			Notes:  "key: value",
			Fields: json.RawMessage(`{"priority":"high","component":"api"}`),
		}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	detail, err := c.GetBead(context.Background(), "bd-abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if detail.ID != "bd-abc123" {
		t.Errorf("expected ID bd-abc123, got %s", detail.ID)
	}
	if detail.Title != "Test bead" {
		t.Errorf("expected title 'Test bead', got %s", detail.Title)
	}
	if detail.Type != "issue" {
		t.Errorf("expected type issue, got %s", detail.Type)
	}
	if detail.Status != "open" {
		t.Errorf("expected status open, got %s", detail.Status)
	}
	if len(detail.Labels) != 2 || detail.Labels[0] != "p1" || detail.Labels[1] != "bug" {
		t.Errorf("expected labels [p1, bug], got %v", detail.Labels)
	}
	if detail.Notes != "key: value" {
		t.Errorf("expected notes 'key: value', got %s", detail.Notes)
	}
	if detail.Fields["priority"] != "high" {
		t.Errorf("expected field priority=high, got %s", detail.Fields["priority"])
	}
	if detail.Fields["component"] != "api" {
		t.Errorf("expected field component=api, got %s", detail.Fields["component"])
	}
}

func TestGetBead_HandlesEmptyFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bead := beadJSON{
			ID:     "bd-empty",
			Title:  "No fields",
			Type:   "issue",
			Status: "open",
		}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	detail, err := c.GetBead(context.Background(), "bd-empty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(detail.Fields) != 0 {
		t.Errorf("expected empty fields, got %v", detail.Fields)
	}
}

func TestGetBead_EscapesBeadID(t *testing.T) {
	var gotRawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.RawPath
		bead := beadJSON{ID: "has/slash"}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "has/slash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The slash should be percent-encoded in the raw URL.
	if gotRawPath != "/api/v1/beads/has%2Fslash" {
		t.Errorf("expected encoded path, got %s", gotRawPath)
	}
}

// --- UpdateBeadFields tests ---

func TestUpdateBeadFields_MergesFields(t *testing.T) {
	var putBody map[string]json.RawMessage
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/beads/"):
			// Return bead with existing fields.
			bead := beadJSON{
				ID:     "bd-merge",
				Fields: json.RawMessage(`{"existing":"keep","overwrite":"old"}`),
			}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/beads/"):
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-merge", map[string]string{
		"overwrite": "new",
		"added":     "fresh",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount != 2 {
		t.Fatalf("expected 2 HTTP calls (GET + PUT), got %d", callCount)
	}

	// Verify the PUT body contains merged fields.
	fieldsRaw, ok := putBody["fields"]
	if !ok {
		t.Fatal("expected 'fields' key in PUT body")
	}
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal merged fields: %v", err)
	}

	if merged["existing"] != "keep" {
		t.Errorf("expected existing field preserved, got %s", merged["existing"])
	}
	if merged["overwrite"] != "new" {
		t.Errorf("expected overwritten field updated, got %s", merged["overwrite"])
	}
	if merged["added"] != "fresh" {
		t.Errorf("expected new field added, got %s", merged["added"])
	}
}

func TestUpdateBeadFields_HandlesNilExistingFields(t *testing.T) {
	var putBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			// Return bead with no fields (nil Fields).
			bead := beadJSON{ID: "bd-nil"}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-nil", map[string]string{
		"new_field": "value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fieldsRaw := putBody["fields"]
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal fields: %v", err)
	}
	if merged["new_field"] != "value" {
		t.Errorf("expected new_field=value, got %s", merged["new_field"])
	}
}

// --- UpdateBeadNotes tests ---

func TestUpdateBeadNotes_SendsCorrectBody(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadNotes(context.Background(), "bd-notes1", "coop_url: http://coop:9090\npod_name: agent-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/api/v1/beads/bd-notes1" {
		t.Errorf("expected path /api/v1/beads/bd-notes1, got %s", gotPath)
	}
	if gotBody["notes"] != "coop_url: http://coop:9090\npod_name: agent-0" {
		t.Errorf("expected notes body, got %v", gotBody)
	}
}

// --- UpdateAgentState tests ---

func TestUpdateAgentState_SetsFieldViaUpdateBeadFields(t *testing.T) {
	var putBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			bead := beadJSON{
				ID:     "bd-state1",
				Fields: json.RawMessage(`{"project":"town"}`),
			}
			json.NewEncoder(w).Encode(bead)

		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			json.Unmarshal(body, &putBody)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateAgentState(context.Background(), "bd-state1", "running")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fieldsRaw := putBody["fields"]
	var merged map[string]string
	if err := json.Unmarshal(fieldsRaw, &merged); err != nil {
		t.Fatalf("failed to unmarshal fields: %v", err)
	}
	if merged["agent_state"] != "running" {
		t.Errorf("expected agent_state=running, got %s", merged["agent_state"])
	}
	if merged["project"] != "town" {
		t.Errorf("expected existing project field preserved, got %s", merged["project"])
	}
}

// --- CloseBead tests ---

func TestCloseBead_SendsCloseRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-close1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("expected POST, got %s", gotMethod)
	}
	if gotPath != "/api/v1/beads/bd-close1/close" {
		t.Errorf("expected path /api/v1/beads/bd-close1/close, got %s", gotPath)
	}
	if gotBody["closed_by"] != "gasboat" {
		t.Errorf("expected closed_by=gasboat, got %v", gotBody)
	}
}

func TestCloseBead_UpdatesFieldsBeforeClose(t *testing.T) {
	var requests []struct {
		Method string
		Path   string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			Method string
			Path   string
		}{r.Method, r.URL.Path})

		switch {
		case r.Method == http.MethodGet:
			bead := beadJSON{
				ID:     "bd-close2",
				Fields: json.RawMessage(`{"existing":"val"}`),
			}
			json.NewEncoder(w).Encode(bead)

		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-close2", map[string]string{"exit_code": "0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be 3 requests: GET (read fields), PUT (update fields), POST (close).
	if len(requests) != 3 {
		t.Fatalf("expected 3 requests (GET+PUT+POST), got %d: %v", len(requests), requests)
	}
	if requests[0].Method != http.MethodGet {
		t.Errorf("first request should be GET, got %s", requests[0].Method)
	}
	if requests[1].Method != http.MethodPut {
		t.Errorf("second request should be PUT, got %s", requests[1].Method)
	}
	if requests[2].Method != http.MethodPost {
		t.Errorf("third request should be POST, got %s", requests[2].Method)
	}
	if requests[2].Path != "/api/v1/beads/bd-close2/close" {
		t.Errorf("third request should be /close, got %s", requests[2].Path)
	}
}

func TestCloseBead_SkipsFieldUpdateWhenEmpty(t *testing.T) {
	var requests []struct {
		Method string
		Path   string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			Method string
			Path   string
		}{r.Method, r.URL.Path})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-close3", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// With nil fields, only the POST /close should happen.
	if len(requests) != 1 {
		t.Fatalf("expected 1 request (POST only), got %d: %v", len(requests), requests)
	}
	if requests[0].Method != http.MethodPost {
		t.Errorf("expected POST, got %s", requests[0].Method)
	}
}

// --- SetConfig tests ---

func TestSetConfig_SendsCorrectRequest(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotBody map[string]json.RawMessage

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.SetConfig(context.Background(), "my-key", []byte(`"my-value"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", gotMethod)
	}
	if gotPath != "/api/v1/config/my-key" {
		t.Errorf("expected path /api/v1/config/my-key, got %s", gotPath)
	}

	valueRaw, ok := gotBody["value"]
	if !ok {
		t.Fatal("expected 'value' key in body")
	}
	if string(valueRaw) != `"my-value"` {
		t.Errorf("expected value '\"my-value\"', got %s", string(valueRaw))
	}
}

func TestSetConfig_EscapesConfigKey(t *testing.T) {
	var gotRawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.RawPath
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.SetConfig(context.Background(), "key/with/slashes", []byte(`"val"`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotRawPath != "/api/v1/config/key%2Fwith%2Fslashes" {
		t.Errorf("expected encoded path, got %s", gotRawPath)
	}
}

// --- Error handling tests ---

func TestAPIError_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "bead not found"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for 404")
	}

	apiErr, ok := err.(*APIError)
	// The error is wrapped by GetBead, so unwrap it.
	if !ok {
		// Check if it's wrapped.
		var inner *APIError
		if unwrapped, ok2 := err.(interface{ Unwrap() error }); ok2 {
			inner, _ = unwrapped.Unwrap().(*APIError)
		}
		if inner == nil {
			t.Fatalf("expected *APIError, got %T: %v", err, err)
		}
		apiErr = inner
	}

	if apiErr.StatusCode != 404 {
		t.Errorf("expected status 404, got %d", apiErr.StatusCode)
	}
	if apiErr.Message != "bead not found" {
		t.Errorf("expected message 'bead not found', got %s", apiErr.Message)
	}
}

func TestAPIError_500WithJSONError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "internal server error"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "bd-500")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "internal server error") {
		t.Errorf("expected error message to contain 'internal server error', got: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

func TestAPIError_500WithPlainTextBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("something went wrong"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.GetBead(context.Background(), "bd-plain")
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Errorf("expected plain text body in error, got: %v", err)
	}
}

func TestAPIError_ErrorStringFormat(t *testing.T) {
	e := &APIError{StatusCode: 422, Message: "invalid fields"}
	got := e.Error()
	want := "HTTP 422: invalid fields"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestDoJSON_204NoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadNotes(context.Background(), "bd-204", "notes")
	if err != nil {
		t.Fatalf("204 No Content should not be an error, got: %v", err)
	}
}

func TestListAgentBeads_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	_, err := c.ListAgentBeads(context.Background())
	if err == nil {
		t.Fatal("expected error for 503")
	}
	if !strings.Contains(err.Error(), "listing agent beads") {
		t.Errorf("expected wrapped error from ListAgentBeads, got: %v", err)
	}
}

func TestUpdateBeadFields_GetFailsPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.UpdateBeadFields(context.Background(), "bd-missing", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when GET fails during field update")
	}
	if !strings.Contains(err.Error(), "reading bead") {
		t.Errorf("expected 'reading bead' in error, got: %v", err)
	}
}

func TestCloseBead_FieldUpdateFailsPropagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// All requests return 500 -- the GET in UpdateBeadFields will fail.
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	err := c.CloseBead(context.Background(), "bd-fail", map[string]string{"k": "v"})
	if err == nil {
		t.Fatal("expected error when field update fails before close")
	}
	if !strings.Contains(err.Error(), "updating fields before close") {
		t.Errorf("expected 'updating fields before close' in error, got: %v", err)
	}
}

// --- parseNotes tests ---

func TestParseNotes_ParsesKeyValueLines(t *testing.T) {
	notes := "coop_url: http://coop:9090\npod_name: agent-hq-0\n"
	m := parseNotes(notes)
	if m["coop_url"] != "http://coop:9090" {
		t.Errorf("expected coop_url, got %v", m)
	}
	if m["pod_name"] != "agent-hq-0" {
		t.Errorf("expected pod_name, got %v", m)
	}
}

func TestParseNotes_HandlesEmptyString(t *testing.T) {
	m := parseNotes("")
	if m != nil {
		t.Errorf("expected nil for empty notes, got %v", m)
	}
}

func TestParseNotes_SkipsBlankLines(t *testing.T) {
	notes := "key1: val1\n\n\nkey2: val2\n"
	m := parseNotes(notes)
	if len(m) != 2 {
		t.Errorf("expected 2 entries, got %d: %v", len(m), m)
	}
}

func TestParseNotes_HandlesColonInValue(t *testing.T) {
	notes := "url: http://host:8080/path"
	m := parseNotes(notes)
	if m["url"] != "http://host:8080/path" {
		t.Errorf("expected URL value preserved, got %s", m["url"])
	}
}

func TestParseNotes_TrimsWhitespace(t *testing.T) {
	notes := "  key  :  value  "
	m := parseNotes(notes)
	if m["key"] != "value" {
		t.Errorf("expected trimmed key/value, got key=%q value=%q", "key", m["key"])
	}
}

func TestParseNotes_NoColonLinesIgnored(t *testing.T) {
	notes := "no-colon-here\nkey: val"
	m := parseNotes(notes)
	if len(m) != 1 {
		t.Errorf("expected 1 entry, got %d: %v", len(m), m)
	}
	if m["key"] != "val" {
		t.Errorf("expected key=val, got %v", m)
	}
}

// --- Content-Type header test ---

func TestDoJSON_SetsContentTypeForBody(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	c.UpdateBeadNotes(context.Background(), "bd-ct", "notes")

	if gotContentType != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", gotContentType)
	}
}

func TestDoJSON_NoContentTypeForGET(t *testing.T) {
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		bead := beadJSON{ID: "bd-get"}
		json.NewEncoder(w).Encode(bead)
	}))
	defer srv.Close()

	c := &Client{baseURL: srv.URL, httpClient: srv.Client()}
	c.GetBead(context.Background(), "bd-get")

	if gotContentType != "" {
		t.Errorf("expected no Content-Type for GET, got %s", gotContentType)
	}
}

// --- BeadLister interface compliance ---

func TestClient_ImplementsBeadLister(t *testing.T) {
	var _ BeadLister = (*Client)(nil)
}
