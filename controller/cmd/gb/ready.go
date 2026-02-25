package main

import (
	"fmt"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var readyCmd = &cobra.Command{
	Use:     "ready",
	Short:   "Show beads ready to work on (open, not blocked)",
	GroupID: "session",
	RunE: func(cmd *cobra.Command, args []string) error {
		beadType, _ := cmd.Flags().GetStringSlice("type")
		assignee, _ := cmd.Flags().GetString("assignee")
		limit, _ := cmd.Flags().GetInt("limit")

		q := beadsapi.ListBeadsQuery{
			Statuses: []string{"open"},
			Types:    beadType,
			Limit:    limit,
			Sort:     "priority",
		}
		if assignee != "" {
			q.Assignee = assignee
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), q)
		if err != nil {
			return fmt.Errorf("listing ready beads: %w", err)
		}

		if jsonOutput {
			printJSON(result.Beads)
		} else if len(result.Beads) == 0 {
			fmt.Println("No beads ready to work on")
		} else {
			for _, b := range result.Beads {
				fmt.Printf("  %s  %s  %s\n", b.ID, b.Title, b.Assignee)
			}
			fmt.Printf("\n%d beads (%d total)\n", len(result.Beads), result.Total)
		}
		return nil
	},
}

func init() {
	readyCmd.Flags().StringSliceP("type", "t", nil, "filter by type (repeatable)")
	readyCmd.Flags().String("assignee", "", "filter by assignee")
	readyCmd.Flags().Int("limit", 20, "maximum number of results")
}
