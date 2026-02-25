package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var decisionCmd = &cobra.Command{
	Use:     "decision",
	Short:   "Manage decision points",
	GroupID: "orchestration",
}

// ── decision create ─────────────────────────────────────────────────────

var decisionCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a decision point and optionally wait for response",
	RunE: func(cmd *cobra.Command, args []string) error {
		prompt, _ := cmd.Flags().GetString("prompt")
		optionsJSON, _ := cmd.Flags().GetString("options")
		requestedBy, _ := cmd.Flags().GetString("requested-by")
		decisionCtx, _ := cmd.Flags().GetString("context")
		noWait, _ := cmd.Flags().GetBool("no-wait")

		if prompt == "" {
			return fmt.Errorf("--prompt is required")
		}

		fields := map[string]any{
			"prompt": prompt,
		}
		if optionsJSON != "" {
			var opts []any
			if err := json.Unmarshal([]byte(optionsJSON), &opts); err != nil {
				return fmt.Errorf("invalid --options JSON: %w", err)
			}
			fields["options"] = json.RawMessage(optionsJSON)
		}
		if decisionCtx != "" {
			fields["context"] = decisionCtx
		}
		if requestedBy == "" {
			requestedBy = actor
		}
		fields["requested_by"] = requestedBy

		agentID, _ := resolveAgentID("")
		if agentID != "" {
			fields["requesting_agent_bead_id"] = agentID
		}

		fieldsJSON, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("encoding fields: %w", err)
		}

		id, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
			Title:     prompt,
			Type:      "decision",
			Kind:      "data",
			Priority:  2,
			CreatedBy: actor,
			Fields:    fieldsJSON,
		})
		if err != nil {
			return fmt.Errorf("creating decision: %w", err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id})
		} else {
			fmt.Printf("Created decision: %s\n", id)
		}

		if noWait {
			return nil
		}

		fmt.Fprintf(os.Stderr, "Waiting for response...\n")
		return waitForDecision(cmd, id)
	},
}

// ── decision list ─────────────────────────────────────────────────────

var decisionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List decision points",
	RunE: func(cmd *cobra.Command, args []string) error {
		status, _ := cmd.Flags().GetStringSlice("status")
		limit, _ := cmd.Flags().GetInt("limit")

		if len(status) == 0 {
			status = []string{"open", "in_progress"}
		}

		result, err := daemon.ListBeadsFiltered(cmd.Context(), beadsapi.ListBeadsQuery{
			Types:    []string{"decision"},
			Statuses: status,
			Limit:    limit,
			Sort:     "-created_at",
		})
		if err != nil {
			return fmt.Errorf("listing decisions: %w", err)
		}

		if jsonOutput {
			printJSON(result.Beads)
		} else if len(result.Beads) == 0 {
			fmt.Println("No pending decisions")
		} else {
			for _, b := range result.Beads {
				printDecisionSummary(b)
				fmt.Println()
			}
		}
		return nil
	},
}

// ── decision show ─────────────────────────────────────────────────────

var decisionShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show details of a decision point",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		bead, err := daemon.GetBead(cmd.Context(), args[0])
		if err != nil {
			return fmt.Errorf("getting decision %s: %w", args[0], err)
		}

		if jsonOutput {
			printJSON(bead)
		} else {
			printDecisionDetail(bead)
		}
		return nil
	},
}

// ── decision respond ──────────────────────────────────────────────────

var decisionRespondCmd = &cobra.Command{
	Use:   "respond <id>",
	Short: "Respond to a decision point",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		selected, _ := cmd.Flags().GetString("select")
		text, _ := cmd.Flags().GetString("text")

		if selected == "" && text == "" {
			return fmt.Errorf("--select or --text is required")
		}

		fields := map[string]string{}
		if selected != "" {
			fields["chosen"] = selected
		}
		if text != "" {
			fields["response_text"] = text
		}
		fields["responded_by"] = actor
		fields["responded_at"] = time.Now().UTC().Format(time.RFC3339)

		if err := daemon.UpdateBeadFields(cmd.Context(), id, fields); err != nil {
			return fmt.Errorf("updating decision %s: %w", id, err)
		}

		if err := daemon.CloseBead(cmd.Context(), id, nil); err != nil {
			return fmt.Errorf("closing decision %s: %w", id, err)
		}

		if jsonOutput {
			printJSON(map[string]string{"id": id, "status": "closed"})
		} else {
			fmt.Printf("Decision %s resolved\n", id)
		}
		return nil
	},
}

// ── helpers ────────────────────────────────────────────────────────────

func printDecisionSummary(b *beadsapi.BeadDetail) {
	prompt := b.Fields["prompt"]
	if prompt == "" {
		prompt = b.Title
	}
	status := b.Status
	chosen := b.Fields["chosen"]
	if chosen != "" {
		status = "resolved: " + chosen
	}

	fmt.Printf("  %s [%s] %s\n", b.ID, status, prompt)

	optionsRaw := b.Fields["options"]
	if optionsRaw != "" {
		var opts []map[string]any
		if err := json.Unmarshal([]byte(optionsRaw), &opts); err == nil {
			for _, opt := range opts {
				id, _ := opt["id"].(string)
				label, _ := opt["label"].(string)
				if label == "" {
					label, _ = opt["short"].(string)
				}
				fmt.Printf("    [%s] %s\n", id, label)
			}
		}
	}
}

func printDecisionDetail(b *beadsapi.BeadDetail) {
	fmt.Printf("ID:       %s\n", b.ID)
	fmt.Printf("Status:   %s\n", b.Status)

	prompt := b.Fields["prompt"]
	if prompt != "" {
		fmt.Printf("Prompt:   %s\n", prompt)
	} else {
		fmt.Printf("Title:    %s\n", b.Title)
	}

	if ctx := b.Fields["context"]; ctx != "" {
		fmt.Printf("Context:  %s\n", ctx)
	}

	optionsRaw := b.Fields["options"]
	if optionsRaw != "" {
		fmt.Println("Options:")
		var opts []map[string]any
		if err := json.Unmarshal([]byte(optionsRaw), &opts); err == nil {
			for _, opt := range opts {
				id, _ := opt["id"].(string)
				label, _ := opt["label"].(string)
				short, _ := opt["short"].(string)
				if label != "" {
					fmt.Printf("  [%s] %s — %s\n", id, short, label)
				} else {
					fmt.Printf("  [%s] %s\n", id, short)
				}
			}
		}
	}

	if chosen := b.Fields["chosen"]; chosen != "" {
		fmt.Printf("Chosen:   %s\n", chosen)
	}
	if respText := b.Fields["response_text"]; respText != "" {
		fmt.Printf("Response: %s\n", respText)
	}
	if respondedBy := b.Fields["responded_by"]; respondedBy != "" {
		fmt.Printf("By:       %s\n", respondedBy)
	}
}

func waitForDecision(cmd *cobra.Command, id string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	// Try SSE first, fall back to polling.
	ch, err := daemon.EventStream(ctx, "beads.>")
	if err != nil {
		return waitDecisionPoll(ctx, id)
	}

	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return waitDecisionPoll(ctx, id)
			}
			var data map[string]any
			if json.Unmarshal(evt.Data, &data) == nil && evt.Event == "beads.bead.closed" {
				// BeadClosed payload: {"bead": {"id": "...", ...}, "closed_by": "..."}
				// (not "bead_id" which is only used by BeadDeleted events)
				if bead, ok := data["bead"].(map[string]any); ok {
					if beadID, _ := bead["id"].(string); beadID == id {
						return printDecisionResult(id)
					}
				}
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func waitDecisionPoll(ctx context.Context, id string) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}

		bead, err := daemon.GetBead(ctx, id)
		if err != nil {
			continue
		}
		if bead.Status == "closed" {
			return printDecisionResult(id)
		}
		if bead.Fields["chosen"] != "" || bead.Fields["response_text"] != "" {
			printDecisionDetail(bead)
			return nil
		}
	}
}

func printDecisionResult(id string) error {
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

// decisionField extracts a field from a BeadDetail's Fields map.
// Used by yield.go for checking decision state.
func decisionField(b *beadsapi.BeadDetail, key string) string {
	return b.Fields[key]
}

// senderFromLabels extracts the sender name from a "from:<name>" label.
func senderFromLabels(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "from:") {
			return strings.TrimPrefix(l, "from:")
		}
	}
	return ""
}

func init() {
	decisionCmd.AddCommand(decisionCreateCmd)
	decisionCmd.AddCommand(decisionListCmd)
	decisionCmd.AddCommand(decisionShowCmd)
	decisionCmd.AddCommand(decisionRespondCmd)

	decisionCreateCmd.Flags().String("prompt", "", "decision prompt (required)")
	decisionCreateCmd.Flags().String("options", "", "options JSON array")
	decisionCreateCmd.Flags().String("requested-by", "", "who is requesting (default: actor)")
	decisionCreateCmd.Flags().String("context", "", "background context for the decision")
	decisionCreateCmd.Flags().Bool("no-wait", false, "return immediately without waiting for response")

	decisionListCmd.Flags().StringSliceP("status", "s", nil, "filter by status")
	decisionListCmd.Flags().Int("limit", 20, "maximum number of results")

	decisionRespondCmd.Flags().String("select", "", "selected option ID")
	decisionRespondCmd.Flags().String("text", "", "free-text response")
}
