package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"gasboat/controller/internal/beadsapi"
)

// EnsureConfigs upserts all gasboat-managed type, view, and context configs
// into the beads daemon.  It is safe to call on every startup; the daemon
// treats SetConfig as an upsert.
//
// For config:* keys, it also creates/updates actual config beads so that
// gb commands (nudge-prompt, prime, setup) can resolve them via
// ResolveConfigBeads.
func EnsureConfigs(ctx context.Context, setter ConfigSetter, logger *slog.Logger) error {
	for key, value := range configs() {
		valueJSON, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("marshalling config %s: %w", key, err)
		}

		// Write to KV store (used by daemon for type/view/context).
		if err := setter.SetConfig(ctx, key, valueJSON); err != nil {
			return fmt.Errorf("setting config %s: %w", key, err)
		}

		// For config:* keys, also upsert a config bead so that
		// ResolveConfigBeads can find them.
		if strings.HasPrefix(key, "config:") {
			if err := ensureConfigBead(ctx, setter, key, valueJSON, logger); err != nil {
				logger.Warn("failed to ensure config bead", "key", key, "error", err)
			}
		}

		logger.Info("ensured beads config", "key", key)
	}

	return nil
}

// parseConfigKey extracts the category and labels from a config key.
// Format: "config:<category>:<scope>" where scope is "global" or "role:<name>".
// Examples:
//
//	"config:nudge-prompts:global"           → ("nudge-prompts", ["global"])
//	"config:claude-instructions:role:thread" → ("claude-instructions", ["role:thread"])
func parseConfigKey(key string) (category string, labels []string) {
	// Strip "config:" prefix.
	rest := strings.TrimPrefix(key, "config:")

	// Split on first ":" to get category.
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return rest, []string{"global"}
	}
	category = rest[:idx]
	scope := rest[idx+1:]

	return category, []string{scope}
}

// ensureConfigBead creates or updates a config bead for the given config key.
func ensureConfigBead(ctx context.Context, setter ConfigSetter, key string, valueJSON []byte, logger *slog.Logger) error {
	category, labels := parseConfigKey(key)

	// Search for an existing open config bead with matching title.
	result, err := setter.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"config"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		return fmt.Errorf("listing config beads: %w", err)
	}

	// Find existing bead with same title and labels.
	description := string(valueJSON)
	for _, bead := range result.Beads {
		if bead.Title == category && labelsMatch(bead.Labels, labels) {
			// Update existing bead's description.
			if bead.Description != description {
				if err := setter.UpdateBeadDescription(ctx, bead.ID, description); err != nil {
					return fmt.Errorf("updating config bead %s: %w", bead.ID, err)
				}
				logger.Info("updated config bead", "key", key, "bead_id", bead.ID)
			}
			return nil
		}
	}

	// Create new config bead.
	id, err := setter.CreateBead(ctx, beadsapi.CreateBeadRequest{
		Title:       category,
		Type:        "config",
		Kind:        "config",
		Description: description,
		Labels:      labels,
		CreatedBy:   "gasboat",
	})
	if err != nil {
		return fmt.Errorf("creating config bead for %s: %w", key, err)
	}
	logger.Info("created config bead", "key", key, "bead_id", id)
	return nil
}

// labelsMatch checks if two label slices contain the same elements.
func labelsMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, l := range a {
		set[l] = true
	}
	for _, l := range b {
		if !set[l] {
			return false
		}
	}
	return true
}
