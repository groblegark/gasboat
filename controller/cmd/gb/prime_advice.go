package main

// prime_advice.go contains outputAdvice and all advice subscription matching helpers.
// These are extracted from prime.go to keep file size manageable.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"gasboat/controller/internal/beadsapi"
)

// outputAdvice fetches open advice beads, filters by agent subscriptions,
// groups by scope, and writes markdown to w.
func outputAdvice(w io.Writer, agentID string) {
	ctx := context.Background()

	result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"advice"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil || len(result.Beads) == 0 {
		return
	}

	subs := buildAgentSubscriptions(agentID, nil)
	subs = enrichAgentSubscriptions(agentID, subs)

	type matchedAdvice struct {
		Bead          *beadsapi.BeadDetail
		MatchedLabels []string
	}
	var matched []matchedAdvice
	for _, bead := range result.Beads {
		if matchesSubscriptions(bead.Labels, subs) {
			ml := findMatchedAdviceLabels(bead.Labels, subs)
			matched = append(matched, matchedAdvice{Bead: bead, MatchedLabels: ml})
		}
	}

	if len(matched) == 0 {
		return
	}

	if jsonOutput {
		type jsonItem struct {
			ID            string   `json:"id"`
			Title         string   `json:"title"`
			Description   string   `json:"description,omitempty"`
			Labels        []string `json:"labels"`
			MatchedLabels []string `json:"matched_labels"`
		}
		items := make([]jsonItem, len(matched))
		for i, m := range matched {
			items[i] = jsonItem{
				ID:            m.Bead.ID,
				Title:         m.Bead.Title,
				Description:   m.Bead.Description,
				Labels:        m.Bead.Labels,
				MatchedLabels: m.MatchedLabels,
			}
		}
		data, _ := json.MarshalIndent(items, "", "  ")
		fmt.Fprintln(w, string(data))
		return
	}

	type scopeGroup struct {
		Scope  string
		Target string
		Header string
		Items  []matchedAdvice
	}

	groupMap := make(map[string]*scopeGroup)
	for _, m := range matched {
		scope, target := categorizeScope(m.Bead.Labels)
		key := scope + ":" + target
		g, ok := groupMap[key]
		if !ok {
			g = &scopeGroup{
				Scope:  scope,
				Target: target,
				Header: buildScopeHeader(scope, target),
			}
			groupMap[key] = g
		}
		g.Items = append(g.Items, m)
	}

	var groups []*scopeGroup
	for _, g := range groupMap {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool {
		return groupSortKey(groups[i].Scope, groups[i].Target) < groupSortKey(groups[j].Scope, groups[j].Target)
	})

	fmt.Fprintf(w, "\n## Advice (%d items)\n\n", len(matched))
	for _, g := range groups {
		for _, item := range g.Items {
			fmt.Fprintf(w, "**[%s]** %s\n", g.Header, item.Bead.Title)
			desc := item.Bead.Description
			if desc != "" && desc != item.Bead.Title {
				for _, line := range strings.Split(desc, "\n") {
					fmt.Fprintf(w, "  %s\n", line)
				}
			}
			fmt.Fprintln(w)
		}
	}
}

// --- Subscription matching (inlined from kbeads internal/model/advice.go) ---

// buildAgentSubscriptions creates auto-subscription labels for an agent.
// Always includes "global" and "agent:<agentID>", plus rig/role labels
// parsed from the agent ID (format: rig/role_plural/name).
func buildAgentSubscriptions(agentID string, extra []string) []string {
	subs := make([]string, 0, len(extra)+4)
	subs = append(subs, extra...)
	subs = append(subs, "global")
	subs = append(subs, "agent:"+agentID)

	parts := strings.Split(agentID, "/")
	if len(parts) >= 1 && parts[0] != "" {
		subs = append(subs, "rig:"+parts[0])
	}
	if len(parts) >= 2 {
		rolePlural := parts[1]
		subs = append(subs, "role:"+rolePlural)
		if roleSingular := singularize(rolePlural); roleSingular != rolePlural {
			subs = append(subs, "role:"+roleSingular)
		}
	}
	return subs
}

// singularize converts a plural role name to singular by stripping a trailing "s".
func singularize(plural string) string {
	if strings.HasSuffix(plural, "s") {
		return strings.TrimSuffix(plural, "s")
	}
	return plural
}

// enrichAgentSubscriptions looks up the agent bead from the daemon and
// adds/removes custom advice subscriptions from the agent's configuration.
func enrichAgentSubscriptions(agentID string, subs []string) []string {
	ctx := context.Background()

	parts := strings.Split(agentID, "/")
	agentName := parts[len(parts)-1]

	agentBead, err := daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		return subs // fail silently
	}

	if raw, ok := agentBead.Fields["advice_subscriptions"]; ok && raw != "" {
		var extra []string
		if json.Unmarshal([]byte(raw), &extra) == nil {
			subs = append(subs, extra...)
		}
	}

	// Derive role: and rig: subscriptions from the agent bead's own labels.
	// This allows role membership to be set at spawn time (e.g. via Slack)
	// by labeling the agent bead, without requiring the agent ID to be
	// formatted as "project/role/name".
	for _, label := range agentBead.Labels {
		if strings.HasPrefix(label, "role:") || strings.HasPrefix(label, "rig:") {
			subs = append(subs, label)
		}
	}

	if raw, ok := agentBead.Fields["advice_subscriptions_exclude"]; ok && raw != "" {
		var exclude []string
		if json.Unmarshal([]byte(raw), &exclude) == nil && len(exclude) > 0 {
			excludeSet := make(map[string]bool, len(exclude))
			for _, exc := range exclude {
				excludeSet[exc] = true
			}
			filtered := subs[:0]
			for _, sub := range subs {
				if !excludeSet[sub] {
					filtered = append(filtered, sub)
				}
			}
			subs = filtered
		}
	}

	return subs
}

// matchesSubscriptions checks if an advice bead should be delivered to an agent
// based on the agent's subscription labels.
func matchesSubscriptions(adviceLabels, subscriptions []string) bool {
	subSet := make(map[string]bool, len(subscriptions))
	for _, s := range subscriptions {
		subSet[s] = true
	}

	// Check required labels: rig:X and agent:X must be in subscriptions.
	for _, l := range adviceLabels {
		clean := stripGroupPrefix(l)
		if strings.HasPrefix(clean, "rig:") && !subSet[clean] {
			return false
		}
		if strings.HasPrefix(clean, "agent:") && !subSet[clean] {
			return false
		}
	}

	// Parse label groups for AND/OR matching.
	groups := parseGroups(adviceLabels)

	// OR across groups: if any group fully matches, advice applies.
	for _, groupLabels := range groups {
		if len(groupLabels) == 0 {
			continue
		}
		allMatch := true
		for _, label := range groupLabels {
			if !subSet[label] {
				allMatch = false
				break
			}
		}
		if allMatch {
			return true
		}
	}
	return false
}

// parseGroups extracts group numbers from label prefixes.
// Labels with gN: prefix are grouped together (AND within group).
// Labels without prefix are treated as separate groups (backward compat - OR behavior).
func parseGroups(labels []string) map[int][]string {
	groups := make(map[int][]string)
	nextUnprefixed := 1000

	for _, label := range labels {
		if strings.HasPrefix(label, "g") {
			idx := strings.Index(label, ":")
			if idx > 1 {
				var groupNum int
				if _, err := fmt.Sscanf(label[:idx], "g%d", &groupNum); err == nil {
					groups[groupNum] = append(groups[groupNum], label[idx+1:])
					continue
				}
			}
		}
		// No valid gN: prefix — treat as its own group (OR behavior).
		groups[nextUnprefixed] = append(groups[nextUnprefixed], label)
		nextUnprefixed++
	}
	return groups
}

// stripGroupPrefix removes the gN: prefix from a label if present.
// "g0:role:polecat" -> "role:polecat", "global" -> "global".
func stripGroupPrefix(label string) string {
	if len(label) >= 3 && label[0] == 'g' {
		for i := 1; i < len(label); i++ {
			if label[i] == ':' && i > 1 {
				return label[i+1:]
			}
			if label[i] < '0' || label[i] > '9' {
				break
			}
		}
	}
	return label
}

// --- Prime helper functions ---

func findMatchedAdviceLabels(adviceLabels, subscriptions []string) []string {
	subSet := make(map[string]bool, len(subscriptions))
	for _, s := range subscriptions {
		subSet[s] = true
	}
	seen := make(map[string]bool)
	var matched []string
	for _, l := range adviceLabels {
		clean := stripGroupPrefix(l)
		if subSet[clean] && !seen[clean] {
			matched = append(matched, clean)
			seen[clean] = true
		}
	}
	return matched
}

func categorizeScope(labels []string) (scope, target string) {
	for _, l := range labels {
		clean := stripGroupPrefix(l)
		switch {
		case strings.HasPrefix(clean, "agent:"):
			return "agent", strings.TrimPrefix(clean, "agent:")
		case strings.HasPrefix(clean, "role:"):
			scope, target = "role", strings.TrimPrefix(clean, "role:")
		case strings.HasPrefix(clean, "rig:") && scope != "role":
			scope, target = "rig", strings.TrimPrefix(clean, "rig:")
		case clean == "global" && scope == "":
			scope, target = "global", ""
		}
	}
	if scope == "" {
		scope = "global"
	}
	return scope, target
}

func buildScopeHeader(scope, target string) string {
	switch scope {
	case "global":
		return "Global"
	case "rig":
		return "Rig: " + target
	case "role":
		return "Role: " + target
	case "agent":
		return "Agent: " + target
	default:
		return scope
	}
}

func groupSortKey(scope, target string) string {
	switch scope {
	case "global":
		return "0:" + target
	case "rig":
		return "1:" + target
	case "role":
		return "2:" + target
	case "agent":
		return "3:" + target
	default:
		return "9:" + target
	}
}
