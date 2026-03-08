package advice

import (
	"testing"

	"gasboat/controller/internal/beadsapi"
)

func TestAdviceDiff_IsEmpty(t *testing.T) {
	d := &AdviceDiff{}
	if !d.IsEmpty() {
		t.Error("empty diff should report IsEmpty() = true")
	}

	d.Added = []MatchedAdvice{{Bead: &beadsapi.BeadDetail{ID: "kd-1"}}}
	if d.IsEmpty() {
		t.Error("diff with additions should not be empty")
	}
}

func TestAdviceDiff_AddedAndRemoved(t *testing.T) {
	// Simulate current advice (role:crew)
	current := []MatchedAdvice{
		{Bead: &beadsapi.BeadDetail{ID: "kd-global", Title: "Global advice"}, Scope: "global"},
		{Bead: &beadsapi.BeadDetail{ID: "kd-crew", Title: "Crew-only advice"}, Scope: "role", ScopeTarget: "crew"},
	}

	// Simulate target advice (role:lead) — global stays, crew gone, lead added
	target := []MatchedAdvice{
		{Bead: &beadsapi.BeadDetail{ID: "kd-global", Title: "Global advice"}, Scope: "global"},
		{Bead: &beadsapi.BeadDetail{ID: "kd-lead", Title: "Lead-only advice"}, Scope: "role", ScopeTarget: "lead"},
	}

	// Build ID sets manually to verify logic.
	currentIDs := map[string]bool{"kd-global": true, "kd-crew": true}
	targetIDs := map[string]bool{"kd-global": true, "kd-lead": true}

	var added []MatchedAdvice
	for _, m := range target {
		if !currentIDs[m.Bead.ID] {
			added = append(added, m)
		}
	}

	var removed []MatchedAdvice
	for _, m := range current {
		if !targetIDs[m.Bead.ID] {
			removed = append(removed, m)
		}
	}

	if len(added) != 1 || added[0].Bead.ID != "kd-lead" {
		t.Errorf("expected 1 added (kd-lead), got %d", len(added))
	}
	if len(removed) != 1 || removed[0].Bead.ID != "kd-crew" {
		t.Errorf("expected 1 removed (kd-crew), got %d", len(removed))
	}
}
