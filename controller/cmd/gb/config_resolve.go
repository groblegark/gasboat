package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"gasboat/controller/internal/advice"
	"gasboat/controller/internal/beadsapi"
)

// configBeadLister is the interface for querying config beads.
// Satisfied by *beadsapi.Client.
type configBeadLister interface {
	ListBeadsFiltered(ctx context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error)
}

// configKVReader is the interface for reading from the legacy KV config store.
// Satisfied by *beadsapi.Client.
type configKVReader interface {
	GetConfig(ctx context.Context, key string) (*beadsapi.ConfigEntry, error)
}

// resolvedConfig holds a matched config bead with its specificity for sorting.
type resolvedConfig struct {
	labels      []string
	value       json.RawMessage
	specificity string // sortable key from advice.GroupSortKey
}

// ResolveConfigBeads queries config beads matching the given category and
// agent subscriptions, sorts by label specificity, and merges according
// to the category's merge strategy.
//
// Returns the merged config and the number of layers found.
// Returns nil, 0 if no matching config beads exist.
func ResolveConfigBeads(ctx context.Context, lister configBeadLister, category string, subscriptions []string) (map[string]any, int) {
	cat := LookupCategory(category)
	if cat == nil {
		return nil, 0
	}

	result, err := lister.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"config"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[config] warning: failed to list config beads: %v\n", err)
		return nil, 0
	}

	// Filter: matching title (category) + matching subscriptions.
	var matched []resolvedConfig
	for _, bead := range result.Beads {
		if bead.Title != category {
			continue
		}
		if !advice.MatchesSubscriptions(bead.Labels, subscriptions) {
			continue
		}

		// Config value is stored in the bead's Description as JSON.
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(bead.Description), &raw); err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: invalid JSON in config bead %s: %v\n", bead.ID, err)
			continue
		}

		scope, target := advice.CategorizeScope(bead.Labels)
		matched = append(matched, resolvedConfig{
			labels:      bead.Labels,
			value:       raw,
			specificity: advice.GroupSortKey(scope, target),
		})
	}

	if len(matched) == 0 {
		return nil, 0
	}

	// Sort by specificity: global (0:) < rig (1:) < role (2:) < agent (3:).
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].specificity < matched[j].specificity
	})

	// Merge layers in specificity order.
	layers := make([]json.RawMessage, len(matched))
	for i, m := range matched {
		layers[i] = m.value
	}

	return MergeLayers(cat.Strategy, layers), len(matched)
}

// ResolveConfigWithFallback tries bead-based resolution first, then falls
// back to the legacy KV config store. This is the Phase 1 migration path.
//
// The role parameter is used for KV fallback (global + role lookup).
// The subscriptions parameter is used for bead-based resolution.
func ResolveConfigWithFallback(
	ctx context.Context,
	lister configBeadLister,
	kvReader configKVReader,
	category string,
	role string,
	subscriptions []string,
) (map[string]any, string) {
	// Try bead-based resolution first.
	merged, count := ResolveConfigBeads(ctx, lister, category, subscriptions)
	if count > 0 {
		fmt.Fprintf(os.Stderr, "[setup] resolved %s from %d config bead(s)\n", category, count)
		return merged, "beads"
	}

	// Fall back to KV config store.
	cat := LookupCategory(category)
	if cat == nil {
		return nil, ""
	}

	var layers []json.RawMessage

	if cfg, err := kvReader.GetConfig(ctx, category+":global"); err == nil && cfg != nil {
		layers = append(layers, cfg.Value)
		fmt.Fprintf(os.Stderr, "[setup] loaded %s:global (KV fallback)\n", category)
	}

	if role != "" {
		if cfg, err := kvReader.GetConfig(ctx, category+":"+role); err == nil && cfg != nil {
			layers = append(layers, cfg.Value)
			fmt.Fprintf(os.Stderr, "[setup] loaded %s:%s (KV fallback)\n", category, role)
		}
	}

	if len(layers) == 0 {
		return nil, ""
	}

	return MergeLayers(cat.Strategy, layers), "kv"
}
