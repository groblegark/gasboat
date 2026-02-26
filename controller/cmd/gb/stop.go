package main

// gb stop — request polite agent despawn.
//
// Sets stop_requested=true on the agent bead. The entrypoint restart loop
// checks this flag after each coop session and exits instead of restarting,
// then closes the bead so the reconciler stops tracking this pod.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Request polite agent despawn — prevents pod restart after exit",
	Long: `Mark this agent as stop-requested.

After calling 'gb stop', exit normally (finish your current turn). The entrypoint
will detect the stop request and close the agent bead instead of restarting,
causing the pod to terminate cleanly.

This is the correct way to despawn an agent voluntarily. Crashing or simply
exiting without calling 'gb stop' will trigger an automatic restart.

Usage:
  gb stop              # request despawn, then exit normally
  gb stop --force      # skip in-progress work check`,
	GroupID: "session",
	RunE:    runStop,
}

var stopForce bool

func init() {
	stopCmd.Flags().BoolVar(&stopForce, "force", false, "skip in-progress work check")
}

func runStop(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	agentID, err := resolveAgentIDWithFallback(ctx, "")
	if err != nil {
		return fmt.Errorf("agent identity required: %w", err)
	}

	// Warn if agent has unclaimed in-progress work (unless --force).
	if !stopForce {
		task, taskErr := daemon.ListAssignedTask(ctx, actor)
		if taskErr == nil && task != nil {
			fmt.Fprintf(os.Stderr, "Warning: you have claimed in-progress work:\n")
			fmt.Fprintf(os.Stderr, "  %s: %s\n\n", task.ID, task.Title)
			fmt.Fprintf(os.Stderr, "Close it first ('kd close %s') or use --force to override.\n", task.ID)
			return fmt.Errorf("claimed work pending — use --force to override")
		}
	}

	if err := daemon.UpdateBeadFields(ctx, agentID, map[string]string{
		"stop_requested": "true",
	}); err != nil {
		return fmt.Errorf("setting stop_requested on bead %s: %w", agentID, err)
	}

	fmt.Printf("Stop requested for agent %s.\n", agentID)
	fmt.Printf("Exit now — the entrypoint will not restart this pod.\n")
	return nil
}
