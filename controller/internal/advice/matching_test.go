package advice

import (
	"testing"
)

func TestMatchesSubscriptions_Global(t *testing.T) {
	labels := []string{"global"}
	subs := []string{"global", "agent:test/crew/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("global advice should match any agent with global subscription")
	}
}

func TestMatchesSubscriptions_RoleMatch(t *testing.T) {
	labels := []string{"role:crew"}
	subs := []string{"global", "role:crew", "agent:gasboat/crews/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("role:crew advice should match agent with role:crew subscription")
	}
}

func TestMatchesSubscriptions_RoleMismatch(t *testing.T) {
	labels := []string{"role:lead"}
	subs := []string{"global", "role:crew", "agent:gasboat/crews/bot"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("role:lead advice should not match agent with only role:crew subscription")
	}
}

func TestMatchesSubscriptions_RigRequired(t *testing.T) {
	labels := []string{"rig:gasboat", "role:crew"}
	subs := []string{"global", "rig:other", "role:crew"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("advice with rig:gasboat should not match agent with rig:other")
	}
}

func TestMatchesSubscriptions_AgentSpecific(t *testing.T) {
	labels := []string{"agent:gasboat/crews/bot"}
	subs := []string{"global", "agent:gasboat/crews/bot"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("agent-specific advice should match the exact agent")
	}
}

func TestMatchesSubscriptions_AgentMismatch(t *testing.T) {
	labels := []string{"agent:gasboat/crews/other"}
	subs := []string{"global", "agent:gasboat/crews/bot"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("agent-specific advice should not match a different agent")
	}
}

func TestMatchesSubscriptions_ANDGroup(t *testing.T) {
	// g0:role:crew AND g0:rig:gasboat -- both must match
	labels := []string{"g0:role:crew", "g0:rig:gasboat"}
	subs := []string{"global", "role:crew", "rig:gasboat"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("AND group should match when all labels in group match")
	}
}

func TestMatchesSubscriptions_ANDGroupPartial(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:rig:gasboat"}
	subs := []string{"global", "role:crew", "rig:other"}
	if MatchesSubscriptions(labels, subs) {
		t.Error("AND group should not match when only some labels match")
	}
}

func TestMatchesSubscriptions_ORGroups(t *testing.T) {
	// Two separate groups: g0:role:crew OR g1:role:lead
	labels := []string{"g0:role:crew", "g1:role:lead"}
	subs := []string{"global", "role:lead"}
	if !MatchesSubscriptions(labels, subs) {
		t.Error("OR across groups should match when any group matches")
	}
}

func TestParseGroups_Grouped(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:rig:gasboat", "g1:role:lead"}
	groups := ParseGroups(labels)

	if len(groups[0]) != 2 {
		t.Errorf("group 0 should have 2 labels, got %d", len(groups[0]))
	}
	if len(groups[1]) != 1 {
		t.Errorf("group 1 should have 1 label, got %d", len(groups[1]))
	}
}

func TestParseGroups_Ungrouped(t *testing.T) {
	labels := []string{"global", "role:crew"}
	groups := ParseGroups(labels)

	// Each ungrouped label gets its own group (starting at 1000)
	count := 0
	for _, v := range groups {
		count += len(v)
	}
	if count != 2 {
		t.Errorf("expected 2 total labels across groups, got %d", count)
	}
}

func TestCategorizeScope_Global(t *testing.T) {
	scope, target := CategorizeScope([]string{"global"})
	if scope != "global" || target != "" {
		t.Errorf("expected global/'', got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Role(t *testing.T) {
	scope, target := CategorizeScope([]string{"global", "role:crew"})
	if scope != "role" || target != "crew" {
		t.Errorf("expected role/crew, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Agent(t *testing.T) {
	scope, target := CategorizeScope([]string{"global", "role:crew", "agent:gasboat/crews/bot"})
	if scope != "agent" || target != "gasboat/crews/bot" {
		t.Errorf("expected agent/gasboat/crews/bot, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_Rig(t *testing.T) {
	scope, target := CategorizeScope([]string{"rig:gasboat"})
	if scope != "rig" || target != "gasboat" {
		t.Errorf("expected rig/gasboat, got %s/%s", scope, target)
	}
}

func TestCategorizeScope_GroupPrefix(t *testing.T) {
	scope, target := CategorizeScope([]string{"g0:role:crew", "g0:rig:gasboat"})
	if scope != "role" || target != "crew" {
		t.Errorf("expected role/crew, got %s/%s", scope, target)
	}
}

func TestBuildAgentSubscriptions(t *testing.T) {
	subs := BuildAgentSubscriptions("gasboat/crews/bot", nil)

	expected := map[string]bool{
		"global":               true,
		"agent:gasboat/crews/bot": true,
		"rig:gasboat":          true,
		"role:crews":           true,
		"role:crew":            true,
	}
	for _, s := range subs {
		delete(expected, s)
	}
	if len(expected) > 0 {
		t.Errorf("missing subscriptions: %v", expected)
	}
}

func TestBuildAgentSubscriptions_SimpleID(t *testing.T) {
	subs := BuildAgentSubscriptions("myrig", nil)

	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["global"] {
		t.Error("should include global")
	}
	if !has["rig:myrig"] {
		t.Error("should include rig:myrig")
	}
}

func TestBuildAgentSubscriptions_WithExtra(t *testing.T) {
	subs := BuildAgentSubscriptions("gasboat/crews/bot", []string{"custom:label"})
	has := make(map[string]bool)
	for _, s := range subs {
		has[s] = true
	}
	if !has["custom:label"] {
		t.Error("should include extra labels")
	}
}

func TestStripGroupPrefix(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"g0:role:crew", "role:crew"},
		{"g12:rig:gasboat", "rig:gasboat"},
		{"global", "global"},
		{"role:crew", "role:crew"},
		{"g:bad", "g:bad"}, // g without number
	}
	for _, tt := range tests {
		got := StripGroupPrefix(tt.input)
		if got != tt.want {
			t.Errorf("StripGroupPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSingularize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"crews", "crew"},
		{"leads", "lead"},
		{"crew", "crew"},
		{"s", ""},
	}
	for _, tt := range tests {
		got := Singularize(tt.input)
		if got != tt.want {
			t.Errorf("Singularize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHasTargetingLabel(t *testing.T) {
	tests := []struct {
		labels []string
		want   bool
	}{
		{[]string{"global"}, true},
		{[]string{"rig:gasboat"}, true},
		{[]string{"role:crew"}, true},
		{[]string{"agent:bot"}, true},
		{[]string{"g0:role:crew"}, true},
		{[]string{"random"}, false},
		{nil, false},
	}
	for _, tt := range tests {
		got := HasTargetingLabel(tt.labels)
		if got != tt.want {
			t.Errorf("HasTargetingLabel(%v) = %v, want %v", tt.labels, got, tt.want)
		}
	}
}

func TestFindMatchedLabels(t *testing.T) {
	labels := []string{"g0:role:crew", "g0:rig:gasboat", "global"}
	subs := []string{"global", "role:crew", "rig:gasboat"}

	matched := FindMatchedLabels(labels, subs)
	if len(matched) != 3 {
		t.Errorf("expected 3 matched labels, got %d: %v", len(matched), matched)
	}
}

func TestBuildScopeHeader(t *testing.T) {
	tests := []struct {
		scope, target, want string
	}{
		{"global", "", "Global"},
		{"rig", "gasboat", "Rig: gasboat"},
		{"role", "crew", "Role: crew"},
		{"agent", "gasboat/crews/bot", "Agent: gasboat/crews/bot"},
	}
	for _, tt := range tests {
		got := BuildScopeHeader(tt.scope, tt.target)
		if got != tt.want {
			t.Errorf("BuildScopeHeader(%q, %q) = %q, want %q", tt.scope, tt.target, got, tt.want)
		}
	}
}

func TestGroupSortKey(t *testing.T) {
	// Global should sort before rig, rig before role, role before agent
	keys := []string{
		GroupSortKey("agent", "bot"),
		GroupSortKey("global", ""),
		GroupSortKey("role", "crew"),
		GroupSortKey("rig", "gasboat"),
	}
	if keys[1] >= keys[3] || keys[3] >= keys[2] || keys[2] >= keys[0] {
		t.Errorf("sort order wrong: global=%s rig=%s role=%s agent=%s",
			keys[1], keys[3], keys[2], keys[0])
	}
}
