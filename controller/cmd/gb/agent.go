package main

import (
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:     "agent",
	Short:   "Agent lifecycle commands",
	GroupID: "agent",
}

func init() {
	agentCmd.AddCommand(agentRosterCmd)
}

var agentRosterCmd = &cobra.Command{
	Use:   "roster",
	Short: "List active agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		agents, err := daemon.ListAgentBeads(ctx)
		if err != nil {
			return err
		}

		if jsonOutput {
			printJSON(agents)
			return nil
		}

		if len(agents) == 0 {
			cmd.Println("No active agents.")
			return nil
		}

		for _, a := range agents {
			state := a.AgentState
			if state == "" {
				state = "unknown"
			}
			cmd.Printf("%-30s %-10s %-10s %s/%s\n",
				a.ID, a.Mode, state, a.Role, a.AgentName)
		}
		return nil
	},
}
