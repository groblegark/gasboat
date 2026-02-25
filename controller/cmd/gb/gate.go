package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var gateAgentID string

var gateCmd = &cobra.Command{
	Use:     "gate",
	Short:   "Manage session gates",
	GroupID: "orchestration",
}

var gateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show gate state for the current agent",
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := resolveGateAgentID(cmd)
		if err != nil {
			return err
		}

		gates, err := daemon.ListGates(cmd.Context(), agentID)
		if err != nil {
			return fmt.Errorf("listing gates: %w", err)
		}

		if jsonOutput {
			printJSON(gates)
			return nil
		}

		if len(gates) == 0 {
			fmt.Printf("No gates found for agent %s.\n", agentID)
			return nil
		}

		fmt.Printf("Session gates for agent %s:\n", agentID)
		for _, g := range gates {
			var bullet string
			var detail string
			if g.Status == "satisfied" {
				bullet = "●"
				if g.SatisfiedAt != nil {
					detail = fmt.Sprintf(" (%s)", g.SatisfiedAt.Format("2006-01-02 15:04:05"))
				}
			} else {
				bullet = "○"
			}
			fmt.Printf("  %s %s: %s%s\n", bullet, g.GateID, g.Status, detail)
		}
		return nil
	},
}

var gateMarkCmd = &cobra.Command{
	Use:   "mark <gate-id>",
	Short: "Manually mark a gate as satisfied",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := resolveGateAgentID(cmd)
		if err != nil {
			return err
		}

		if err := daemon.SatisfyGate(cmd.Context(), agentID, args[0]); err != nil {
			return fmt.Errorf("satisfying gate: %w", err)
		}

		fmt.Printf("✓ Gate %s marked as satisfied\n", args[0])
		return nil
	},
}

var gateClearCmd = &cobra.Command{
	Use:   "clear <gate-id>",
	Short: "Clear a gate (reset to pending)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, err := resolveGateAgentID(cmd)
		if err != nil {
			return err
		}

		if err := daemon.ClearGate(cmd.Context(), agentID, args[0]); err != nil {
			return fmt.Errorf("clearing gate: %w", err)
		}

		fmt.Printf("○ Gate %s cleared (pending)\n", args[0])
		return nil
	},
}

// resolveGateAgentID resolves the agent bead ID for gate operations.
func resolveGateAgentID(cmd *cobra.Command) (string, error) {
	return resolveAgentIDWithFallback(cmd.Context(), gateAgentID)
}

func init() {
	gateCmd.PersistentFlags().StringVar(&gateAgentID, "agent-id", "", "agent bead ID (default: KD_AGENT_ID env)")

	gateCmd.AddCommand(gateStatusCmd)
	gateCmd.AddCommand(gateMarkCmd)
	gateCmd.AddCommand(gateClearCmd)
}
