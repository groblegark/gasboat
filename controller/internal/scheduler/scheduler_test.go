package scheduler

import (
	"context"
	"sync"
	"testing"
	"time"

	"log/slog"

	"gasboat/controller/internal/beadsapi"
)

// mockSpawner records spawn calls.
type mockSpawner struct {
	mu     sync.Mutex
	calls  []spawnCall
	fields map[string]map[string]string
}

type spawnCall struct {
	agentName string
	project   string
	taskID    string
	role      string
	prompt    string
}

func (m *mockSpawner) SpawnAgent(_ context.Context, agentName, project, taskID, role, prompt string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, spawnCall{agentName, project, taskID, role, prompt})
	return "kd-spawned-123", nil
}

func (m *mockSpawner) UpdateBeadFields(_ context.Context, beadID string, fields map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fields == nil {
		m.fields = make(map[string]map[string]string)
	}
	m.fields[beadID] = fields
	return nil
}

func (m *mockSpawner) spawnCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// mockLister returns a fixed set of schedule beads.
type mockLister struct {
	beads []beadsapi.ScheduleBead
}

func (m *mockLister) ListScheduleBeads(_ context.Context) ([]beadsapi.ScheduleBead, error) {
	return m.beads, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&discardWriter{}, nil))
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestSync_AddsNewEntry(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{{
			ID:      "kd-sched-1",
			Title:   "Daily Report",
			Project: "gasboat",
			Cron:    "0 0 * * *",
			Role:    "crew",
			Prompt:  "Generate daily report",
			Enabled: true,
		}},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	if s.EntryCount() != 1 {
		t.Errorf("expected 1 entry, got %d", s.EntryCount())
	}
}

func TestSync_RemovesDisabledEntry(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{{
			ID:      "kd-sched-1",
			Title:   "Daily Report",
			Project: "gasboat",
			Cron:    "0 0 * * *",
			Enabled: true,
		}},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.EntryCount() != 1 {
		t.Fatalf("expected 1 entry after first sync, got %d", s.EntryCount())
	}

	// Disable the schedule.
	lister.beads[0].Enabled = false
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.EntryCount() != 0 {
		t.Errorf("expected 0 entries after disable, got %d", s.EntryCount())
	}
}

func TestSync_UpdatesChangedCron(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{{
			ID:      "kd-sched-1",
			Title:   "Daily Report",
			Project: "gasboat",
			Cron:    "0 0 * * *",
			Enabled: true,
		}},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Change the cron expression.
	lister.beads[0].Cron = "0 6 * * *"
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s.EntryCount() != 1 {
		t.Errorf("expected 1 entry after update, got %d", s.EntryCount())
	}

	// Verify the entry was updated.
	s.mu.Lock()
	e := s.entries["kd-sched-1"]
	s.mu.Unlock()
	if e.cronExp != "0 6 * * *" {
		t.Errorf("expected cron '0 6 * * *', got %q", e.cronExp)
	}
}

func TestSync_SkipsInvalidCron(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{{
			ID:      "kd-sched-1",
			Title:   "Bad Cron",
			Project: "gasboat",
			Cron:    "not a cron expression",
			Enabled: true,
		}},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s.EntryCount() != 0 {
		t.Errorf("expected 0 entries for invalid cron, got %d", s.EntryCount())
	}
}

func TestFire_SpawnsAgent(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{}
	s := New(spawner, lister, testLogger())

	bead := beadsapi.ScheduleBead{
		ID:      "kd-sched-1",
		Title:   "Daily Report",
		Project: "gasboat",
		Role:    "crew",
		Prompt:  "Generate daily report",
	}

	s.fire("kd-sched-1", bead)

	if spawner.spawnCount() != 1 {
		t.Fatalf("expected 1 spawn call, got %d", spawner.spawnCount())
	}

	call := spawner.calls[0]
	if call.project != "gasboat" {
		t.Errorf("expected project 'gasboat', got %q", call.project)
	}
	if call.role != "crew" {
		t.Errorf("expected role 'crew', got %q", call.role)
	}
	if call.prompt != "Generate daily report" {
		t.Errorf("expected prompt 'Generate daily report', got %q", call.prompt)
	}
	if call.taskID != "" {
		t.Errorf("expected empty taskID, got %q", call.taskID)
	}

	// Verify last_run was updated.
	spawner.mu.Lock()
	fields := spawner.fields["kd-sched-1"]
	spawner.mu.Unlock()
	if fields["last_run"] == "" {
		t.Error("expected last_run to be set")
	}
	if fields["last_agent_id"] != "kd-spawned-123" {
		t.Errorf("expected last_agent_id 'kd-spawned-123', got %q", fields["last_agent_id"])
	}
}

func TestGenerateAgentName(t *testing.T) {
	tests := []struct {
		title    string
		wantPfx  string
	}{
		{"Daily Release Notes", "daily-release-notes-"},
		{"Weekly Summary", "weekly-summary-"},
		{"", "scheduled-"},
		{"one", "one-"},
	}
	for _, tt := range tests {
		got := generateAgentName(tt.title)
		if len(got) < len(tt.wantPfx) || got[:len(tt.wantPfx)] != tt.wantPfx {
			t.Errorf("generateAgentName(%q) = %q, want prefix %q", tt.title, got, tt.wantPfx)
		}
	}
}

func TestSync_MultipleBead(t *testing.T) {
	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{
			{ID: "kd-s1", Title: "Report A", Project: "gasboat", Cron: "0 0 * * *", Enabled: true},
			{ID: "kd-s2", Title: "Report B", Project: "monorepo", Cron: "0 6 * * 1", Enabled: true},
			{ID: "kd-s3", Title: "Disabled", Project: "gasboat", Cron: "0 0 * * *", Enabled: false},
		},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if s.EntryCount() != 2 {
		t.Errorf("expected 2 entries (1 disabled), got %d", s.EntryCount())
	}
}

func TestSync_EveryMinuteCron_Fires(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test")
	}

	spawner := &mockSpawner{}
	lister := &mockLister{
		beads: []beadsapi.ScheduleBead{{
			ID:      "kd-sched-1",
			Title:   "Fast Schedule",
			Project: "gasboat",
			Cron:    "@every 1s",
			Role:    "crew",
			Prompt:  "test prompt",
			Enabled: true,
		}},
	}
	s := New(spawner, lister, testLogger())
	s.Start()
	defer s.Stop()

	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Wait for at least one fire.
	time.Sleep(2 * time.Second)

	if spawner.spawnCount() < 1 {
		t.Errorf("expected at least 1 spawn after 2s with @every 1s, got %d", spawner.spawnCount())
	}
}
