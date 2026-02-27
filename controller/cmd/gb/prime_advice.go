package main

// prime_advice.go contains outputAdvice which renders matched advice for an agent.
// Subscription matching logic lives in internal/advice/.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"gasboat/controller/internal/advice"
)

// outputAdvice fetches open advice beads, filters by agent subscriptions,
// groups by scope, and writes markdown to w.
func outputAdvice(w io.Writer, agentID string) {
	matched, _, err := advice.ListAdviceForAgent(context.Background(), daemon, agentID)
	if err != nil || len(matched) == 0 {
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
		Items  []advice.MatchedAdvice
	}

	groupMap := make(map[string]*scopeGroup)
	for _, m := range matched {
		key := m.Scope + ":" + m.ScopeTarget
		g, ok := groupMap[key]
		if !ok {
			g = &scopeGroup{
				Scope:  m.Scope,
				Target: m.ScopeTarget,
				Header: m.ScopeHeader,
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
		return advice.GroupSortKey(groups[i].Scope, groups[i].Target) < advice.GroupSortKey(groups[j].Scope, groups[j].Target)
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
