package main

// gb agent spawn <name> [project] [--role <role>] [--task <bead-id>] [--prompt "..."]
//
// Creates an agent bead that the reconciler picks up and schedules as a K8s pod.
// This is the CLI equivalent of the Slack /spawn slash command.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var agentSpawnCmd = &cobra.Command{
	Use:   "spawn <name> [project]",
	Short: "Create an agent bead for remote agent creation",
	Long: `Create an agent bead that the reconciler picks up and schedules as a K8s pod.
This is the CLI equivalent of the Slack /spawn slash command.

  <name>       Agent name (required, positional).
  [project]    Project name (optional positional, default: $BOAT_PROJECT).
  --role       Agent role (default: crew).
  --task       Pre-assign a task bead ID. Sets task_id in fields and adds
               an 'assigned' dependency linking the agent to the task.
  --prompt     Custom prompt for the agent session (becomes BOAT_PROMPT env var).

Example:

  gb agent spawn my-bot gasboat --role=crew --task=kd-123 --prompt="Fix the login bug"`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runAgentSpawn,
}

func init() {
	agentCmd.AddCommand(agentSpawnCmd)

	agentSpawnCmd.Flags().String("role", "crew", "Agent role (e.g. crew, captain, job)")
	agentSpawnCmd.Flags().String("task", "", "Pre-assign a task bead ID")
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
	customPrompt, _ := cmd.Flags().GetString("prompt")

	// Build agent fields.
	fields := map[string]string{
		"agent":   agentName,
		"mode":    "crew",
		"role":    role,
		"project": project,
	}
	if taskID != "" {
		fields["task_id"] = taskID
	}
	if customPrompt != "" {
		fields["prompt"] = customPrompt
	}

	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshalling fields: %w", err)
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	req := beadsapi.CreateBeadRequest{
		Title:     agentName,
		Type:      "agent",
		CreatedBy: actor,
		Fields:    fieldsJSON,
	}
	if taskID != "" {
		req.Description = "Assigned to task: " + taskID
	} else if customPrompt != "" {
		req.Description = customPrompt
	}

	id, err := daemon.CreateBead(ctx, req)
	if err != nil {
		return fmt.Errorf("creating agent bead: %w", err)
	}

	// Best-effort labels for project and role filtering.
	if project != "" {
		_ = daemon.AddLabel(ctx, id, "project:"+project)
	}
	_ = daemon.AddLabel(ctx, id, "role:"+role)

	// Best-effort: link task dependency if provided.
	if taskID != "" {
		_ = daemon.AddDependency(ctx, id, taskID, "assigned", actor)
	}

	if jsonOutput {
		out := map[string]string{
			"id":   id,
			"name": agentName,
		}
		if taskID != "" {
			out["task"] = taskID
		}
		printJSON(out)
		return nil
	}

	fmt.Printf("Agent bead created: %s\n", id)
	fmt.Printf("  Name:    %s\n", agentName)
	fmt.Printf("  Project: %s\n", project)
	fmt.Printf("  Role:    %s\n", role)
	if taskID != "" {
		fmt.Printf("  Task:    %s\n", taskID)
	}
	if customPrompt != "" {
		fmt.Printf("  Prompt:  %s\n", customPrompt)
	}
	fmt.Println("\nThe reconciler will schedule a pod for this agent.")

	return nil
}
