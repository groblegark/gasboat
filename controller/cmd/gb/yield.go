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

var yieldAgentID string

var yieldCmd = &cobra.Command{
	Use:   "yield",
	Short: "Block until a pending decision is resolved or mail arrives",
	Long: `Blocks the agent until one of the following events occurs:
  - A pending decision bead (type=decision, status=open) is closed/resolved
  - A mail/message bead targeting this agent is created
  - The timeout expires (default 24h)

Uses HTTP SSE for real-time notification, with 2-second polling as fallback.

Requires an open decision bead created with 'gb decision create' before
calling yield. After the decision resolves, gb yield calls
POST /v1/agents/{id}/gates/decision/satisfy to release the Stop gate.`,
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

		// Resolve agent ID — required for gate satisfaction.
		agentID, err := resolveAgentIDWithFallback(ctx, yieldAgentID)
		if err != nil {
			return fmt.Errorf("agent identity required for yield: %w", err)
		}

		// Find open decision beads for this agent.
		result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
			Statuses: []string{"open"},
			Types:    []string{"decision"},
			Sort:     "-created_at",
			Limit:    20,
		})
		if err != nil {
			return fmt.Errorf("listing decisions: %w", err)
		}

		// Find a decision that belongs to this agent.
		var pending *beadsapi.BeadDetail
		for _, b := range result.Beads {
			if b.Fields["requesting_agent_bead_id"] == agentID {
				pending = b
				break
			}
		}

		if pending == nil {
			return fmt.Errorf("no open decision for agent %s; create one with 'gb decision create'", agentID)
		}

		prompt := pending.Fields["prompt"]
		if prompt == "" {
			prompt = pending.Title
		}
		fmt.Fprintf(os.Stderr, "Yielding on decision %s: %s\n", pending.ID, prompt)

		if err := yieldSSE(ctx, []*beadsapi.BeadDetail{pending}); err != nil {
			return err
		}

		// Only satisfy the gate if the yield resolved normally (not timed out or interrupted).
		if ctx.Err() != nil {
			return nil
		}

		// Re-fetch the resolved decision to check if an artifact is required.
		// If so, the gate stays pending until 'gb decision report' submits the artifact.
		resolvedBead, fetchErr := daemon.GetBead(context.Background(), pending.ID)
		if fetchErr == nil && resolvedBead.Fields["required_artifact"] != "" {
			fmt.Fprintf(os.Stderr, "Artifact required (%s) — gate stays pending until 'gb decision report %s' is called\n",
				resolvedBead.Fields["required_artifact"], pending.ID)
			return nil
		}

		// Call satisfy gate — this releases the Stop hook for the next session turn.
		satisfyCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := daemon.SatisfyGate(satisfyCtx, agentID, "decision"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to satisfy decision gate: %v\n", err)
		}

		return nil
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
				// BeadClosed/BeadCreated/BeadUpdated: id and type are nested under "bead".
				// BeadDeleted: id is at top-level "bead_id".
				var beadID, beadType string
				if bead, ok := data["bead"].(map[string]any); ok {
					beadID, _ = bead["id"].(string)
					beadType, _ = bead["type"].(string)
				} else {
					beadID, _ = data["bead_id"].(string)
				}
				if pendingIDs[beadID] && evt.Event == "beads.bead.closed" {
					return printYieldResult(beadID)
				}
				if evt.Event == "beads.bead.created" && (beadType == "message" || beadType == "mail") {
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
	}
}

func printYieldResult(id string) error {
	bead, err := daemon.GetBead(context.Background(), id)
	if err != nil {
		return err
	}
	chosen := bead.Fields["chosen"]
	responseText := bead.Fields["response_text"]
	rationale := bead.Fields["rationale"]

	result := chosen
	if result == "" {
		result = responseText
	}
	if result != "" {
		if rationale != "" {
			fmt.Printf("Decision %s resolved: %s — %s\n", id, result, rationale)
		} else {
			fmt.Printf("Decision %s resolved: %s\n", id, result)
		}
	} else {
		fmt.Printf("Decision %s closed\n", id)
	}

	// Check if the chosen option requires an artifact (set by gb decision respond).
	if ra := bead.Fields["required_artifact"]; ra != "" {
		fmt.Printf("ARTIFACT_REQUIRED type=%s decision_id=%s\n", ra, id)
		return fmt.Errorf("decision %s requires a %s artifact — run: gb decision report %s --type %s --content '...'", id, ra, id, ra)
	}

	return nil
}

func init() {
	yieldCmd.Flags().Duration("timeout", 24*time.Hour, "maximum time to wait")
	yieldCmd.Flags().StringVar(&yieldAgentID, "agent-id", "", "agent bead ID (default: KD_AGENT_ID env)")
}
