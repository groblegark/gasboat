package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

// spawnTestRequest captures method, path, and parsed body from an HTTP request.
type spawnTestRequest struct {
	method string
	path   string
	body   map[string]json.RawMessage
}

// setupSpawnDaemon creates an httptest server that records all requests and
// returns a stable bead ID for POST /v1/beads. It sets the package-level
// daemon and returns a cleanup function plus the recorded requests slice.
func setupSpawnDaemon(t *testing.T, beadID string) (*[]spawnTestRequest, func()) {
	t.Helper()

	var requests []spawnTestRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var parsed map[string]json.RawMessage
		_ = json.Unmarshal(body, &parsed)
		requests = append(requests, spawnTestRequest{r.Method, r.URL.Path, parsed})

		if r.URL.Path == "/v1/beads" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": beadID})
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}))

	oldDaemon := daemon
	oldActor := actor
	c, err := beadsapi.New(beadsapi.Config{HTTPAddr: srv.URL})
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	daemon = c
	actor = "test-actor"

	return &requests, func() {
		daemon = oldDaemon
		actor = oldActor
		srv.Close()
	}
}

func TestAgentSpawn_BasicCreation(t *testing.T) {
	requests, cleanup := setupSpawnDaemon(t, "bd-spawn-1")
	defer cleanup()

	t.Setenv("BOAT_PROJECT", "")

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := runAgentSpawn(agentSpawnCmd, []string{"my-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := *requests
	// Expect 3 requests: create bead, add project label, add role label.
	if len(reqs) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(reqs))
	}

	// Verify create bead request.
	createReq := reqs[0]
	if createReq.method != http.MethodPost {
		t.Errorf("expected POST, got %s", createReq.method)
	}
	if createReq.path != "/v1/beads" {
		t.Errorf("expected /v1/beads, got %s", createReq.path)
	}

	var beadType, beadTitle string
	_ = json.Unmarshal(createReq.body["type"], &beadType)
	_ = json.Unmarshal(createReq.body["title"], &beadTitle)
	if beadType != "agent" {
		t.Errorf("expected type=agent, got %s", beadType)
	}
	if beadTitle != "my-bot" {
		t.Errorf("expected title=my-bot, got %s", beadTitle)
	}

	var fields map[string]string
	_ = json.Unmarshal(createReq.body["fields"], &fields)
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
		t.Errorf("expected fields.role=crew (default), got %s", fields["role"])
	}

	// Verify project label.
	var projectLabel string
	_ = json.Unmarshal(reqs[1].body["label"], &projectLabel)
	if projectLabel != "project:gasboat" {
		t.Errorf("expected label=project:gasboat, got %s", projectLabel)
	}

	// Verify role label.
	var roleLabel string
	_ = json.Unmarshal(reqs[2].body["label"], &roleLabel)
	if roleLabel != "role:crew" {
		t.Errorf("expected label=role:crew, got %s", roleLabel)
	}
}

func TestAgentSpawn_WithTask(t *testing.T) {
	requests, cleanup := setupSpawnDaemon(t, "bd-spawn-2")
	defer cleanup()

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	agentSpawnCmd.Flags().Set("task", "kd-task-123")
	defer agentSpawnCmd.Flags().Set("task", "")

	err := runAgentSpawn(agentSpawnCmd, []string{"task-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := *requests
	// Expect 4 requests: create bead, project label, role label, dependency.
	if len(reqs) != 4 {
		t.Fatalf("expected 4 requests, got %d", len(reqs))
	}

	// Verify task_id in fields.
	var fields map[string]string
	_ = json.Unmarshal(reqs[0].body["fields"], &fields)
	if fields["task_id"] != "kd-task-123" {
		t.Errorf("expected fields.task_id=kd-task-123, got %s", fields["task_id"])
	}

	// Verify description references the task.
	var desc string
	_ = json.Unmarshal(reqs[0].body["description"], &desc)
	if desc != "Assigned to task: kd-task-123" {
		t.Errorf("expected description 'Assigned to task: kd-task-123', got %q", desc)
	}

	// Verify dependency request.
	depReq := reqs[3]
	if depReq.path != "/v1/beads/bd-spawn-2/dependencies" {
		t.Errorf("expected dep path /v1/beads/bd-spawn-2/dependencies, got %s", depReq.path)
	}
	var dependsOn, depType string
	_ = json.Unmarshal(depReq.body["depends_on_id"], &dependsOn)
	_ = json.Unmarshal(depReq.body["type"], &depType)
	if dependsOn != "kd-task-123" {
		t.Errorf("expected depends_on_id=kd-task-123, got %s", dependsOn)
	}
	if depType != "assigned" {
		t.Errorf("expected dep type=assigned, got %s", depType)
	}
}

func TestAgentSpawn_WithPrompt(t *testing.T) {
	requests, cleanup := setupSpawnDaemon(t, "bd-spawn-3")
	defer cleanup()

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	agentSpawnCmd.Flags().Set("prompt", "Fix the login bug")
	defer agentSpawnCmd.Flags().Set("prompt", "")

	err := runAgentSpawn(agentSpawnCmd, []string{"prompt-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := *requests
	var fields map[string]string
	_ = json.Unmarshal(reqs[0].body["fields"], &fields)
	if fields["prompt"] != "Fix the login bug" {
		t.Errorf("expected fields.prompt='Fix the login bug', got %s", fields["prompt"])
	}

	// Verify description is set to prompt when no task.
	var desc string
	_ = json.Unmarshal(reqs[0].body["description"], &desc)
	if desc != "Fix the login bug" {
		t.Errorf("expected description='Fix the login bug', got %q", desc)
	}
}

func TestAgentSpawn_DefaultProjectFromEnv(t *testing.T) {
	requests, cleanup := setupSpawnDaemon(t, "bd-spawn-4")
	defer cleanup()

	t.Setenv("BOAT_PROJECT", "myproject")

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := runAgentSpawn(agentSpawnCmd, []string{"env-bot"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := *requests
	var fields map[string]string
	_ = json.Unmarshal(reqs[0].body["fields"], &fields)
	if fields["project"] != "myproject" {
		t.Errorf("expected fields.project=myproject, got %s", fields["project"])
	}

	// Verify project label uses env value.
	var projectLabel string
	_ = json.Unmarshal(reqs[1].body["label"], &projectLabel)
	if projectLabel != "project:myproject" {
		t.Errorf("expected label=project:myproject, got %s", projectLabel)
	}
}

func TestAgentSpawn_JSONOutput(t *testing.T) {
	_, cleanup := setupSpawnDaemon(t, "bd-spawn-5")
	defer cleanup()

	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runAgentSpawn(agentSpawnCmd, []string{"json-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var parsed map[string]string
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got: %q (error: %v)", output, err)
	}
	if parsed["id"] != "bd-spawn-5" {
		t.Errorf("expected id=bd-spawn-5, got %s", parsed["id"])
	}
	if parsed["name"] != "json-bot" {
		t.Errorf("expected name=json-bot, got %s", parsed["name"])
	}
}

func TestAgentSpawn_JSONOutputWithTask(t *testing.T) {
	_, cleanup := setupSpawnDaemon(t, "bd-spawn-6")
	defer cleanup()

	origJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = origJSON }()

	agentSpawnCmd.Flags().Set("task", "kd-task-456")
	defer agentSpawnCmd.Flags().Set("task", "")

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runAgentSpawn(agentSpawnCmd, []string{"json-task-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := strings.TrimSpace(string(buf[:n]))

	var parsed map[string]string
	if err := json.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got: %q (error: %v)", output, err)
	}
	if parsed["task"] != "kd-task-456" {
		t.Errorf("expected task=kd-task-456, got %s", parsed["task"])
	}
}

func TestAgentSpawn_CreateError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
	}))
	defer srv.Close()

	oldDaemon := daemon
	oldActor := actor
	c, err := beadsapi.New(beadsapi.Config{HTTPAddr: srv.URL})
	if err != nil {
		t.Fatalf("failed to create test client: %v", err)
	}
	daemon = c
	actor = "test-actor"
	defer func() { daemon = oldDaemon; actor = oldActor }()

	spawnErr := runAgentSpawn(agentSpawnCmd, []string{"bad-bot", "gasboat"})
	if spawnErr == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(spawnErr.Error(), "creating agent bead") {
		t.Errorf("expected error about creating agent bead, got: %v", spawnErr)
	}
}

func TestAgentSpawn_CustomRole(t *testing.T) {
	requests, cleanup := setupSpawnDaemon(t, "bd-spawn-7")
	defer cleanup()

	agentSpawnCmd.Flags().Set("role", "captain")
	defer agentSpawnCmd.Flags().Set("role", "crew")

	// Capture stdout.
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	err := runAgentSpawn(agentSpawnCmd, []string{"captain-bot", "gasboat"})

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reqs := *requests
	var fields map[string]string
	_ = json.Unmarshal(reqs[0].body["fields"], &fields)
	if fields["role"] != "captain" {
		t.Errorf("expected fields.role=captain, got %s", fields["role"])
	}

	// Verify role label.
	foundRoleLabel := false
	for _, req := range reqs[1:] {
		var label string
		_ = json.Unmarshal(req.body["label"], &label)
		if label == "role:captain" {
			foundRoleLabel = true
		}
	}
	if !foundRoleLabel {
		t.Error("expected role:captain label to be added")
	}
}
