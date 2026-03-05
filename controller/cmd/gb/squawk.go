package main

import (
	"encoding/json"
	"fmt"

	"gasboat/controller/internal/beadsapi"

	"github.com/spf13/cobra"
)

var squawkCmd = &cobra.Command{
	Use:   "squawk <message>",
	Short: "Send an informational message to the operator",
	Long: `Posts a short informational message that the operator will see in Slack.

The message is stored as a bead (type=message) and relayed to the
appropriate Slack thread by the bridge. The agent does not need to know
about Slack — it just "squawks" something.

Good for: progress updates, completion notices, findings worth flagging.
Not for: questions (use gb decision), long reports (use gb decision report),
or agent-to-agent comms (use gb mail send).`,
	GroupID: "orchestration",
	Args:   cobra.ExactArgs(1),
	RunE:   runSquawk,
}

func runSquawk(cmd *cobra.Command, args []string) error {
	text := args[0]
	if text == "" {
		return fmt.Errorf("message text cannot be empty")
	}

	// Truncate title to 500 chars (bead title limit).
	title := text
	if len(title) > 500 {
		title = title[:497] + "..."
	}

	fields := map[string]string{
		"source_agent": actor,
		"text":         text,
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshalling fields: %w", err)
	}

	// Create the message bead.
	beadID, err := daemon.CreateBead(cmd.Context(), beadsapi.CreateBeadRequest{
		Title:     title,
		Type:      "message",
		Kind:      "data",
		Labels:    []string{"squawk", fmt.Sprintf("from:%s", actor)},
		Fields:    json.RawMessage(fieldsJSON),
		CreatedBy: actor,
	})
	if err != nil {
		return fmt.Errorf("creating message bead: %w", err)
	}

	// Immediately close the bead (fire-and-forget).
	if err := daemon.CloseBead(cmd.Context(), beadID, nil); err != nil {
		return fmt.Errorf("closing message bead: %w", err)
	}

	if jsonOutput {
		printJSON(map[string]string{"id": beadID, "status": "sent"})
	} else {
		fmt.Println("OK")
	}
	return nil
}
