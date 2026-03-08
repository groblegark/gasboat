package main

import (
	"testing"
)

func TestDetectRoleMismatch_NoMismatch(t *testing.T) {
	labels := []string{"project:gasboat", "role:crew"}
	got := detectRoleMismatch(labels, "crew")
	if got != "" {
		t.Errorf("expected no mismatch, got %q", got)
	}
}

func TestDetectRoleMismatch_PluralMatch(t *testing.T) {
	// Agent has role "crew", bead has "role:crews" (plural) — should not mismatch
	// because Singularize("crews") = "crew".
	labels := []string{"role:crews"}
	got := detectRoleMismatch(labels, "crew")
	if got != "" {
		t.Errorf("expected no mismatch for plural form, got %q", got)
	}
}

func TestDetectRoleMismatch_DifferentRole(t *testing.T) {
	labels := []string{"project:gasboat", "role:lead"}
	got := detectRoleMismatch(labels, "crew")
	if got != "lead" {
		t.Errorf("expected mismatch role=lead, got %q", got)
	}
}

func TestDetectRoleMismatch_MultiRole(t *testing.T) {
	// Agent has "crew,thread" — both roles should be accepted.
	labels := []string{"role:thread"}
	got := detectRoleMismatch(labels, "crew,thread")
	if got != "" {
		t.Errorf("expected no mismatch for multi-role agent, got %q", got)
	}
}

func TestDetectRoleMismatch_NoRoleLabel(t *testing.T) {
	labels := []string{"project:gasboat"}
	got := detectRoleMismatch(labels, "crew")
	if got != "" {
		t.Errorf("expected no mismatch when bead has no role label, got %q", got)
	}
}

func TestParseRoles(t *testing.T) {
	roles := parseRoles("crew,thread")
	if !roles["crew"] {
		t.Error("expected crew in parsed roles")
	}
	if !roles["thread"] {
		t.Error("expected thread in parsed roles")
	}
	// Singularized forms should also be present.
	if !roles["crew"] { // "crew" is already singular
		t.Error("expected singular crew in parsed roles")
	}
}

func TestParseRoles_SingleRole(t *testing.T) {
	roles := parseRoles("crews")
	if !roles["crews"] {
		t.Error("expected crews in parsed roles")
	}
	if !roles["crew"] {
		t.Error("expected singular crew in parsed roles")
	}
}
