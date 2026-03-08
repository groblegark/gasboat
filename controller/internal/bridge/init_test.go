package bridge

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"gasboat/controller/internal/beadsapi"
)

type mockConfigSetter struct {
	configs map[string][]byte
	beads   []*beadsapi.BeadDetail
}

func (m *mockConfigSetter) SetConfig(_ context.Context, key string, value []byte) error {
	m.configs[key] = value
	return nil
}

func (m *mockConfigSetter) CreateBead(_ context.Context, req beadsapi.CreateBeadRequest) (string, error) {
	id := "kd-mock-" + req.Title
	m.beads = append(m.beads, &beadsapi.BeadDetail{
		ID:          id,
		Title:       req.Title,
		Type:        req.Type,
		Description: req.Description,
		Labels:      req.Labels,
		Status:      "open",
	})
	return id, nil
}

func (m *mockConfigSetter) ListBeadsFiltered(_ context.Context, _ beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error) {
	return &beadsapi.ListBeadsResult{Beads: m.beads, Total: len(m.beads)}, nil
}

func (m *mockConfigSetter) UpdateBeadDescription(_ context.Context, beadID, description string) error {
	for _, b := range m.beads {
		if b.ID == beadID {
			b.Description = description
			break
		}
	}
	return nil
}

func TestEnsureConfigs_SeedsNudgePrompts(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}
	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	raw, ok := setter.configs["config:nudge-prompts:global"]
	if !ok {
		t.Fatal("expected config:nudge-prompts:global to be seeded")
	}

	var prompts map[string]string
	if err := json.Unmarshal(raw, &prompts); err != nil {
		t.Fatalf("unmarshal nudge-prompts: %v", err)
	}

	for _, key := range []string{"thread", "adhoc", "default", "prewarmed"} {
		if prompts[key] == "" {
			t.Errorf("nudge-prompts missing key %q", key)
		}
	}

	// Adhoc prompt should use template variable, not hardcoded "Create a bead".
	if val := prompts["adhoc"]; val != "" {
		if strings.Contains(val, "Create a bead") {
			t.Error("adhoc prompt should not say 'Create a bead' — should use {{.TaskHint}}")
		}
		if !strings.Contains(val, "{{.TaskHint}}") {
			t.Error("adhoc prompt should contain {{.TaskHint}} template variable")
		}
		if !strings.Contains(val, "{{.BoatPrompt}}") {
			t.Error("adhoc prompt should contain {{.BoatPrompt}} template variable")
		}
	}
}

func TestEnsureConfigs_SeedsClaudeInstructions(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}
	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	raw, ok := setter.configs["config:claude-instructions:global"]
	if !ok {
		t.Fatal("expected config:claude-instructions:global to be seeded")
	}

	var sections map[string]string
	if err := json.Unmarshal(raw, &sections); err != nil {
		t.Fatalf("unmarshal claude-instructions: %v", err)
	}

	expectedKeys := []string{
		"prime_header", "session_close", "core_rules", "commands",
		"workflows", "decisions", "session_resumption", "lifecycle",
		"stop_gate", "stop_gate_blocked",
	}
	for _, key := range expectedKeys {
		if sections[key] == "" {
			t.Errorf("claude-instructions missing key %q", key)
		}
	}

	// stop_gate_blocked should contain system-reminder tags.
	if val := sections["stop_gate_blocked"]; !strings.Contains(val, "<system-reminder>") {
		t.Error("stop_gate_blocked should contain <system-reminder> tags")
	}
}

func TestEnsureConfigs_SeedsRoleOverrides(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}
	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	for _, role := range []string{"thread", "polecat"} {
		key := "config:claude-instructions:role:" + role
		raw, ok := setter.configs[key]
		if !ok {
			t.Errorf("expected %s to be seeded", key)
			continue
		}

		var sections map[string]string
		if err := json.Unmarshal(raw, &sections); err != nil {
			t.Errorf("unmarshal %s: %v", key, err)
			continue
		}

		if sections["commands"] == "" {
			t.Errorf("%s missing commands section", key)
		}
		if sections["lifecycle"] == "" {
			t.Errorf("%s missing lifecycle section", key)
		}
	}

	// Thread lifecycle should mention "stay alive" / not gb done.
	raw := setter.configs["config:claude-instructions:role:thread"]
	var threadSections map[string]string
	_ = json.Unmarshal(raw, &threadSections)
	if !strings.Contains(threadSections["lifecycle"], "stay alive") {
		t.Error("thread lifecycle should mention staying alive")
	}

	// Polecat lifecycle should mention single-task.
	raw = setter.configs["config:claude-instructions:role:polecat"]
	var polecatSections map[string]string
	_ = json.Unmarshal(raw, &polecatSections)
	if !strings.Contains(polecatSections["lifecycle"], "single-task") {
		t.Error("polecat lifecycle should mention single-task")
	}
}

func TestEnsureConfigs_SeedsAllExpectedTypes(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}
	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	for _, key := range []string{
		"type:agent", "type:project", "type:task",
		"view:agents:active", "view:decisions:pending",
		"context:crew",
	} {
		if _, ok := setter.configs[key]; !ok {
			t.Errorf("expected config %q to be seeded", key)
		}
	}
}

func TestEnsureConfigs_CreatesConfigBeads(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}
	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	// Should have created config beads for all config:* keys.
	expectedBeads := map[string][]string{
		"nudge-prompts":       {"global"},
		"claude-instructions": {"global"},
	}
	roleBeads := map[string]string{
		"role:thread": "claude-instructions",
		"role:polecat": "claude-instructions",
	}

	for title, wantLabels := range expectedBeads {
		found := false
		for _, b := range setter.beads {
			if b.Title == title && labelsMatch(b.Labels, wantLabels) {
				found = true
				var m map[string]any
				if err := json.Unmarshal([]byte(b.Description), &m); err != nil {
					t.Errorf("config bead %s has invalid JSON description: %v", title, err)
				}
				break
			}
		}
		if !found {
			t.Errorf("expected config bead with title=%q labels=%v", title, wantLabels)
		}
	}

	for label, title := range roleBeads {
		found := false
		for _, b := range setter.beads {
			if b.Title == title && labelsMatch(b.Labels, []string{label}) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected config bead with title=%q label=%q", title, label)
		}
	}
}

func TestEnsureConfigs_UpdatesExistingConfigBead(t *testing.T) {
	setter := &mockConfigSetter{configs: make(map[string][]byte)}

	// Pre-seed a config bead that EnsureConfigs should update.
	setter.beads = append(setter.beads, &beadsapi.BeadDetail{
		ID:          "kd-existing",
		Title:       "nudge-prompts",
		Type:        "config",
		Description: `{"old":"value"}`,
		Labels:      []string{"global"},
		Status:      "open",
	})

	if err := EnsureConfigs(context.Background(), setter, slog.Default()); err != nil {
		t.Fatalf("EnsureConfigs: %v", err)
	}

	// Should have updated the existing bead, not created a new one.
	nudgeCount := 0
	for _, b := range setter.beads {
		if b.Title == "nudge-prompts" && labelsMatch(b.Labels, []string{"global"}) {
			nudgeCount++
		}
	}
	if nudgeCount != 1 {
		t.Errorf("expected 1 nudge-prompts bead (upsert), got %d", nudgeCount)
	}

	// The description should be updated to the current value.
	for _, b := range setter.beads {
		if b.ID == "kd-existing" {
			var prompts map[string]string
			if err := json.Unmarshal([]byte(b.Description), &prompts); err != nil {
				t.Fatalf("unmarshal updated description: %v", err)
			}
			if prompts["thread"] == "" {
				t.Error("expected thread prompt in updated description")
			}
			break
		}
	}
}

func TestParseConfigKey(t *testing.T) {
	tests := []struct {
		key        string
		wantCat    string
		wantLabels []string
	}{
		{"config:nudge-prompts:global", "nudge-prompts", []string{"global"}},
		{"config:claude-instructions:global", "claude-instructions", []string{"global"}},
		{"config:claude-instructions:role:thread", "claude-instructions", []string{"role:thread"}},
		{"config:claude-instructions:role:polecat", "claude-instructions", []string{"role:polecat"}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			cat, labels := parseConfigKey(tt.key)
			if cat != tt.wantCat {
				t.Errorf("category: got %q, want %q", cat, tt.wantCat)
			}
			if !labelsMatch(labels, tt.wantLabels) {
				t.Errorf("labels: got %v, want %v", labels, tt.wantLabels)
			}
		})
	}
}
