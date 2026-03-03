package main

import (
	"context"
	"encoding/json"
	"fmt"
	"gasboat/controller/internal/beadsapi"
	"os"
	"sort"

	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:     "config",
	Short:   "Config bead management",
	GroupID: "session",
}

// configNamespaces returns the known config bead namespaces to dump.
// Delegates to the config registry (config_registry.go).
func configNamespaces() []string {
	return ConfigCategoryNames()
}

// --- Serialization types ---

type configEntry struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type adviceEntry struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels,omitempty"`
	Priority    int      `json:"priority"`
}

// configDump is the top-level dump/load format.
type configDump struct {
	Configs []configEntry `json:"configs"`
	Advice  []adviceEntry `json:"advice,omitempty"`
}

// --- Interfaces for testability ---

type configDumper interface {
	ListConfigs(ctx context.Context, namespace string) ([]beadsapi.ConfigEntry, error)
}

type beadLister interface {
	ListBeadsFiltered(ctx context.Context, q beadsapi.ListBeadsQuery) (*beadsapi.ListBeadsResult, error)
}

type configLoader interface {
	SetConfig(ctx context.Context, key string, value []byte) error
}

// --- Core logic ---

func dumpConfigs(ctx context.Context, client configDumper, namespaces []string) ([]configEntry, error) {
	var entries []configEntry
	for _, ns := range namespaces {
		configs, err := client.ListConfigs(ctx, ns)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to list %s: %v\n", ns, err)
			continue
		}
		for _, c := range configs {
			entries = append(entries, configEntry{Key: c.Key, Value: c.Value})
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Key < entries[j].Key
	})

	return entries, nil
}

func dumpAdvice(ctx context.Context, client beadLister) ([]adviceEntry, error) {
	result, err := client.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"advice"},
		Statuses: []string{"open"},
		Limit:    500,
	})
	if err != nil {
		return nil, fmt.Errorf("listing advice: %w", err)
	}

	entries := make([]adviceEntry, 0, len(result.Beads))
	for _, b := range result.Beads {
		entries = append(entries, adviceEntry{
			Title:       b.Title,
			Description: b.Description,
			Labels:      b.Labels,
			Priority:    b.Priority,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Title < entries[j].Title
	})

	return entries, nil
}

func loadConfigs(ctx context.Context, client configLoader, entries []configEntry) (restored int, errors int) {
	for _, e := range entries {
		if err := client.SetConfig(ctx, e.Key, e.Value); err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to set %s: %v\n", e.Key, err)
			errors++
			continue
		}
		fmt.Fprintf(os.Stderr, "[config] restored %s\n", e.Key)
		restored++
	}
	return restored, errors
}

// --- Commands ---

var configDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Dump all configs and advice as JSON (pipe to file for backup)",
	Long: `Fetches config entries from all known namespaces and all advice
beads, then prints them as a JSON object with "configs" and "advice"
sections. The output is suitable for saving to a file and restoring
later with 'gb config load'.

Sections:
  configs — key/value config beads (settings, hooks, MCP, types, views)
  advice  — advice beads (rules and guidance for agents)

Example:
  gb config dump > backup.json
  gb config dump | jq .advice`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		configs, err := dumpConfigs(ctx, daemon, configNamespaces())
		if err != nil {
			return err
		}

		advice, err := dumpAdvice(ctx, daemon)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[config] warning: failed to dump advice: %v\n", err)
		}

		dump := configDump{
			Configs: configs,
			Advice:  advice,
		}

		data, err := json.MarshalIndent(dump, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling: %w", err)
		}

		fmt.Fprintln(os.Stdout, string(data))
		fmt.Fprintf(os.Stderr, "[config] dumped %d configs, %d advice\n", len(configs), len(advice))
		return nil
	},
}

var configLoadCmd = &cobra.Command{
	Use:   "load <file>",
	Short: "Restore configs and advice from a dump file",
	Long: `Reads a JSON file produced by 'gb config dump' and restores
config entries and advice beads to the daemon.

Advice beads are matched by title — existing advice with the same
title is skipped to avoid duplicates.

Example:
  gb config load backup.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		data, err := os.ReadFile(args[0])
		if err != nil {
			return fmt.Errorf("reading file: %w", err)
		}

		var dump configDump
		if err := json.Unmarshal(data, &dump); err != nil {
			// Try legacy format (flat array of config entries).
			var legacy []configEntry
			if err2 := json.Unmarshal(data, &legacy); err2 == nil {
				dump.Configs = legacy
			} else {
				return fmt.Errorf("parsing JSON: %w", err)
			}
		}

		restored, _ := loadConfigs(ctx, daemon, dump.Configs)
		fmt.Fprintf(os.Stderr, "[config] loaded %d config entries\n", restored)

		if len(dump.Advice) > 0 {
			// Build set of existing advice titles for dedup.
			existing, _ := dumpAdvice(ctx, daemon)
			titleSet := make(map[string]bool, len(existing))
			for _, a := range existing {
				titleSet[a.Title] = true
			}

			created := 0
			for _, a := range dump.Advice {
				if titleSet[a.Title] {
					fmt.Fprintf(os.Stderr, "[config] advice exists, skipping: %s\n", a.Title)
					continue
				}
				_, err := daemon.CreateBead(ctx, beadsapi.CreateBeadRequest{
				Title:       a.Title,
				Type:        "advice",
				Description: a.Description,
				Labels:      a.Labels,
				Priority:    a.Priority,
			})
			if err != nil {
					fmt.Fprintf(os.Stderr, "[config] warning: failed to create advice %q: %v\n", a.Title, err)
					continue
				}
				fmt.Fprintf(os.Stderr, "[config] created advice: %s\n", a.Title)
				created++
			}
			fmt.Fprintf(os.Stderr, "[config] loaded %d advice beads (%d skipped)\n", created, len(dump.Advice)-created)
		}

		return nil
	},
}

func init() {
	configCmd.AddCommand(configDumpCmd)
	configCmd.AddCommand(configLoadCmd)
}
