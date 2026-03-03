package main

// gb agent spawn <name> [project] [--role <role>] [--task <bead-id>] [--prompt "..."]
//
// Creates an agent bead that the reconciler picks up and schedules as a
// K8s pod. This is the CLI equivalent of the Slack /spawn slash command.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var agentSpawnCmd = &cobra.Command{
	Use:   "spawn <name> [project]",
	Short: "Spawn a remote agent pod via the reconciler",
	Long: `Create an agent bead that the reconciler picks up and schedules
as a K8s pod running Claude Code. This is the CLI equivalent of the
Slack /spawn slash command.

  <name>      Agent name (required). Lowercase letters, digits, hyphens.
  [project]   Project name (optional, default: $BOAT_PROJECT).

Examples:

  gb agent spawn slack-impl gasboat
  gb agent spawn my-bot gasboat --role crew --task kd-abc123
  gb agent spawn fixer --prompt "Fix the flaky auth test in login_test.go"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAgentSpawn,
}

func init() {
	agentCmd.AddCommand(agentSpawnCmd)

	agentSpawnCmd.Flags().String("role", "crew", "Agent role (e.g. crew, captain, job)")
	agentSpawnCmd.Flags().String("task", "", "Pre-assign a task bead ID to the agent")
	agentSpawnCmd.Flags().String("prompt", "", "Custom prompt for the agent session")
}

func runAgentSpawn(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	project := ""
	if len(args) >= 2 {
		project = args[1]
	}
	if project == "" {
		project = os.Getenv("BOAT_PROJECT")
	}

	role, _ := cmd.Flags().GetString("role")
	taskID, _ := cmd.Flags().GetString("task")
	prompt, _ := cmd.Flags().GetString("prompt")

	// Build agent fields.
	fields := map[string]string{
		"agent":   agentName,
		"mode":    "crew",
		"role":    role,
		"project": project,
	}
	if prompt != "" {
		fields["prompt"] = prompt
	}

	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshalling fields: %w", err)
	}

	req := beadsapi.CreateBeadRequest{
		Title:     agentName,
		Type:      "agent",
		CreatedBy: actor,
		Fields:    fieldsJSON,
	}
	if taskID != "" {
		req.Description = "Assigned to task: " + taskID
	} else if prompt != "" {
		req.Description = prompt
	}

	ctx := cmd.Context()

	id, err := daemon.CreateBead(ctx, req)
	if err != nil {
		return fmt.Errorf("creating agent bead: %w", err)
	}

	// Best-effort labels for project and role filtering.
	if project != "" {
		_ = daemon.AddLabel(ctx, id, "project:"+project)
	}
	_ = daemon.AddLabel(ctx, id, "role:"+role)

	// Best-effort task assignment dependency.
	if taskID != "" {
		if err := daemon.AddDependency(ctx, id, taskID, "assigned", actor); err != nil {
			slog.Warn("failed to add task dependency to agent bead",
				"agent", agentName, "bead", id, "task", taskID, "error", err)
		}
	}

	if jsonOutput {
		result := map[string]string{
			"id":      id,
			"name":    agentName,
			"project": project,
			"role":    role,
		}
		if taskID != "" {
			result["task"] = taskID
		}
		printJSON(result)
		return nil
	}

	fmt.Printf("Agent bead created: %s\n", id)
	fmt.Printf("  Name:    %s\n", agentName)
	fmt.Printf("  Project: %s\n", project)
	fmt.Printf("  Role:    %s\n", role)
	if taskID != "" {
		fmt.Printf("  Task:    %s\n", taskID)
	}
	if prompt != "" {
		preview := prompt
		if len(preview) > 80 {
			preview = preview[:77] + "..."
		}
		fmt.Printf("  Prompt:  %s\n", preview)
	}
	fmt.Println("\nThe reconciler will schedule a pod for this agent.")

	return nil
}
