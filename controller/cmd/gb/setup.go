package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var setupCmd = &cobra.Command{
	Use:     "setup",
	Short:   "Setup commands for agent environment",
	GroupID: "session",
}

// gbHookCommand is the command prefix for gb bus emit hooks.
const gbHookCommand = "gb bus emit --hook="

var setupClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "Materialize Claude Code hooks from config beads",
	Long: `Fetches claude-hooks config beads from the daemon, merges them by
specificity (global → role → agent), and writes .claude/settings.json
in the workspace directory.

Config bead keys (checked in order, later overrides earlier):
  claude-hooks:global   — base hooks for all agents
  claude-hooks:<role>   — role-specific overrides

Flags:
  --defaults   Install hardcoded default hooks (no server needed)
  --check      Verify hooks are installed, exit 1 if missing
  --remove     Remove gb hooks from settings.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		workspace, _ := cmd.Flags().GetString("workspace")
		if workspace == "" {
			workspace, _ = os.Getwd()
		}

		role, _ := cmd.Flags().GetString("role")
		if role == "" {
			role = os.Getenv("BOAT_ROLE")
		}
		if role == "" {
			role = os.Getenv("KD_ROLE")
		}

		check, _ := cmd.Flags().GetBool("check")
		if check {
			return runSetupClaudeCheck(workspace)
		}

		remove, _ := cmd.Flags().GetBool("remove")
		if remove {
			return runSetupClaudeRemove(workspace)
		}

		defaults, _ := cmd.Flags().GetBool("defaults")
		if defaults {
			return runSetupClaudeDefaults(workspace)
		}

		return runSetupClaude(cmd, workspace, role)
	},
}

func init() {
	setupClaudeCmd.Flags().String("workspace", os.Getenv("KD_WORKSPACE"), "workspace directory")
	setupClaudeCmd.Flags().String("role", "", "agent role (default: $BOAT_ROLE or $KD_ROLE)")
	setupClaudeCmd.Flags().Bool("defaults", false, "install hardcoded default hooks (no server needed)")
	setupClaudeCmd.Flags().Bool("check", false, "verify hooks are installed (exit 1 if missing)")
	setupClaudeCmd.Flags().Bool("remove", false, "remove gb hooks from settings.json")
	setupCmd.AddCommand(setupClaudeCmd)
}

func defaultHookSettings() map[string]any {
	hookTypes := []string{"SessionStart", "PreCompact", "Stop", "PreToolUse", "PostToolUse"}
	hooks := make(map[string]any, len(hookTypes))
	for _, ht := range hookTypes {
		hooks[ht] = []any{
			map[string]any{
				"matcher": "",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"command": gbHookCommand + ht,
					},
				},
			},
		}
	}
	return map[string]any{
		"hooks": hooks,
	}
}

func runSetupClaudeDefaults(workspace string) error {
	settings := defaultHookSettings()

	outDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	outPath := filepath.Join(outDir, "settings.json")
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote default hooks to %s\n", outPath)
	return nil
}

func runSetupClaudeCheck(workspace string) error {
	settingsPath := filepath.Join(workspace, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("hooks not installed: %s not found", settingsPath)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("hooks not installed: invalid JSON in %s", settingsPath)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return fmt.Errorf("hooks not installed: no hooks key in %s", settingsPath)
	}

	required := []string{"SessionStart", "Stop"}
	for _, ht := range required {
		if !hookContainsGB(hooks, ht) {
			return fmt.Errorf("hooks not installed: missing %s hook with gb command", ht)
		}
	}

	fmt.Fprintf(os.Stderr, "hooks OK: gb hooks installed in %s\n", settingsPath)
	return nil
}

func hookContainsGB(hooks map[string]any, hookType string) bool {
	arr, ok := hooks[hookType].([]any)
	if !ok {
		return false
	}
	for _, entry := range arr {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, ok := entryMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range innerHooks {
			hMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			cmd, _ := hMap["command"].(string)
			if strings.HasPrefix(cmd, gbHookCommand) {
				return true
			}
		}
	}
	return false
}

func runSetupClaudeRemove(workspace string) error {
	settingsPath := filepath.Join(workspace, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return fmt.Errorf("no settings file: %w", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}

	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		fmt.Fprintf(os.Stderr, "[setup] no hooks to remove\n")
		return nil
	}

	removed := 0
	for hookType, val := range hooks {
		arr, ok := val.([]any)
		if !ok {
			continue
		}
		var filtered []any
		for _, entry := range arr {
			entryMap, ok := entry.(map[string]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			innerHooks, ok := entryMap["hooks"].([]any)
			if !ok {
				filtered = append(filtered, entry)
				continue
			}
			hasGB := false
			for _, h := range innerHooks {
				hMap, ok := h.(map[string]any)
				if !ok {
					continue
				}
				cmd, _ := hMap["command"].(string)
				if strings.HasPrefix(cmd, gbHookCommand) {
					hasGB = true
					break
				}
			}
			if !hasGB {
				filtered = append(filtered, entry)
			} else {
				removed++
			}
		}
		if len(filtered) == 0 {
			delete(hooks, hookType)
		} else {
			hooks[hookType] = filtered
		}
	}

	if removed == 0 {
		fmt.Fprintf(os.Stderr, "[setup] no gb hooks found to remove\n")
		return nil
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(settingsPath, out, 0600); err != nil {
		return fmt.Errorf("writing: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] removed %d gb hook(s) from %s\n", removed, settingsPath)
	return nil
}

func runSetupClaude(cmd *cobra.Command, workspace, role string) error {
	var layers []json.RawMessage

	if cfg, err := daemon.GetConfig(cmd.Context(), "claude-hooks:global"); err == nil && cfg != nil {
		layers = append(layers, cfg.Value)
		fmt.Fprintf(os.Stderr, "[setup] loaded claude-hooks:global\n")
	}

	if role != "" {
		if cfg, err := daemon.GetConfig(cmd.Context(), "claude-hooks:"+role); err == nil && cfg != nil {
			layers = append(layers, cfg.Value)
			fmt.Fprintf(os.Stderr, "[setup] loaded claude-hooks:%s\n", role)
		}
	}

	if len(layers) == 0 {
		return fmt.Errorf("no claude-hooks config beads found")
	}

	merged := mergeHookLayers(layers)

	outDir := filepath.Join(workspace, ".claude")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("creating .claude dir: %w", err)
	}

	outPath := filepath.Join(outDir, "settings.json")
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling settings: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(outPath, data, 0600); err != nil {
		return fmt.Errorf("writing settings: %w", err)
	}

	fmt.Fprintf(os.Stderr, "[setup] wrote %s\n", outPath)
	return nil
}

func mergeHookLayers(layers []json.RawMessage) map[string]any {
	result := make(map[string]any)
	for _, raw := range layers {
		var layer map[string]any
		if err := json.Unmarshal(raw, &layer); err != nil {
			continue
		}
		mergeSettingsLayer(result, layer)
	}
	return result
}

func mergeSettingsLayer(dst, src map[string]any) {
	for k, v := range src {
		if k == "hooks" {
			srcHooks, ok := v.(map[string]any)
			if !ok {
				dst[k] = v
				continue
			}
			dstHooks, ok := dst[k].(map[string]any)
			if !ok {
				dstHooks = make(map[string]any)
				dst[k] = dstHooks
			}
			mergeHooksField(dstHooks, srcHooks)
		} else {
			dst[k] = v
		}
	}
}

func mergeHooksField(dst, src map[string]any) {
	for hookType, srcVal := range src {
		srcArr, ok := srcVal.([]any)
		if !ok {
			dst[hookType] = srcVal
			continue
		}
		dstArr, ok := dst[hookType].([]any)
		if !ok {
			dst[hookType] = srcArr
			continue
		}
		dst[hookType] = append(dstArr, srcArr...)
	}
}
