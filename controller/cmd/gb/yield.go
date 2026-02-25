package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var yieldCmd = &cobra.Command{
	Use:   "yield",
	Short: "Block until a pending decision is resolved or mail arrives",
	Long: `Blocks the agent until one of the following events occurs:
  - A pending decision bead (type=decision, status=open) is closed/resolved
  - A mail/message bead targeting this agent is created
  - The timeout expires (default 24h)

Uses HTTP SSE for real-time notification, with 2-second polling as fallback.`,
	GroupID: "session",
	RunE: func(cmd *cobra.Command, args []string) error {
		timeout, _ := cmd.Flags().GetDuration("timeout")

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()

		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Statuses: []string{"open"},
			Types:    []string{"decision"},
			Sort:     "-created_at",
			Limit:    1,
		})
		if err != nil {
			return fmt.Errorf("listing decisions: %w", err)
		}

		if len(result.Beads) == 0 {
			fmt.Println("No pending decisions found, waiting for any event...")
		} else {
			d := result.Beads[0]
			prompt := d.Fields["prompt"]
			if prompt == "" {
				prompt = d.Title
			}
			fmt.Fprintf(os.Stderr, "Yielding on decision %s: %s\n", d.ID, prompt)
		}

		return yieldSSE(ctx, result.Beads)
	},
}

func yieldSSE(ctx context.Context, pending []*beadsapi.BeadDetail) error {
	pendingIDs := make(map[string]bool, len(pending))
	for _, b := range pending {
		pendingIDs[b.ID] = true
	}

	ch, err := daemon.EventStream(ctx, "beads.>")
	if err != nil {
		return yieldPoll(ctx, pendingIDs)
	}

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return yieldPoll(ctx, pendingIDs)
			}
			var data map[string]any
			if json.Unmarshal(evt.Data, &data) == nil {
				beadID, _ := data["bead_id"].(string)
				if pendingIDs[beadID] {
					return printYieldResult(beadID)
				}
				beadType, _ := data["type"].(string)
				if beadType == "message" || beadType == "mail" {
					fmt.Printf("Mail received: %s\n", beadID)
					return nil
				}
			}
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				fmt.Println("Yield timed out")
			}
			return nil
		}
	}
}

func yieldPoll(ctx context.Context, pendingIDs map[string]bool) error {
	for {
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				fmt.Println("Yield timed out")
			}
			return nil
		case <-time.After(2 * time.Second):
		}

		for id := range pendingIDs {
			bead, err := daemon.GetBead(ctx, id)
			if err != nil {
				continue
			}
			if bead.Status == "closed" {
				return printYieldResult(id)
			}
			if bead.Fields["chosen"] != "" || bead.Fields["response_text"] != "" {
				return printYieldResult(id)
			}
		}

		if len(pendingIDs) == 0 {
			msgs, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
				Statuses: []string{"open"},
				Types:    []string{"message", "mail"},
				Limit:    1,
				Sort:     "-created_at",
			})
			if err == nil && len(msgs.Beads) > 0 {
				fmt.Printf("Mail received: %s\n", msgs.Beads[0].ID)
				return nil
			}
		}
	}
}

func printYieldResult(id string) error {
	bead, err := daemon.GetBead(context.Background(), id)
	if err != nil {
		return err
	}
	chosen := bead.Fields["chosen"]
	responseText := bead.Fields["response_text"]
	if chosen != "" {
		fmt.Printf("Decision %s resolved: %s\n", id, chosen)
	} else if responseText != "" {
		fmt.Printf("Decision %s resolved: %s\n", id, responseText)
	} else {
		fmt.Printf("Decision %s closed\n", id)
	}
	return nil
}

func init() {
	yieldCmd.Flags().Duration("timeout", 24*time.Hour, "maximum time to wait")
}
