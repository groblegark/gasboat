package main

// gb hook relay — Publish Claude Code hook events to NATS for doots visualization.
//
// Reads the hook input JSON from stdin, transforms it to the hook event schema
// (see docs/hook-event-schema.md), and publishes to the HOOK_EVENTS NATS
// stream on subject hooks.<agent>.<event>.
//
// Env vars:
//   BEADS_NATS_URL  — NATS server URL (e.g., nats://gasboat-nats:4222)
//   HOSTNAME        — Agent name for the subject hierarchy

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/cobra"
)

var hookRelayCmd = &cobra.Command{
	Use:   "relay",
	Short: "Publish hook events to NATS for doots visualization",
	Long: `Reads Claude Code hook input JSON from stdin and publishes it
to the HOOK_EVENTS NATS JetStream stream.

Subject format: hooks.<agent>.<event>
Example: hooks.worker-1.PreToolUse

Events published: PreToolUse, PostToolUse, Stop, SubagentStart,
SubagentStop, SessionStart, SessionEnd, PreCompact.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runHookRelay()
	},
}

func init() {
	hookCmd.AddCommand(hookRelayCmd)
}

// hookRelayEvent is the NATS message payload for hook events.
type hookRelayEvent struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Event     string `json:"event"`
	TS        string `json:"ts"`
	CWD       string `json:"cwd,omitempty"`

	// Tool events
	ToolName     string `json:"tool_name,omitempty"`
	ToolInput    any    `json:"tool_input,omitempty"`
	ToolResponse any    `json:"tool_response,omitempty"`

	// Session events
	Source string `json:"source,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Subagent events
	SubagentID   string `json:"subagent_id,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`

	// Compact events
	Trigger string `json:"trigger,omitempty"`
}

func runHookRelay() error {
	natsURL := os.Getenv("BEADS_NATS_URL")
	if natsURL == "" {
		return nil // No NATS configured — silently skip.
	}

	// Read hook input from stdin.
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		return nil // No input — nothing to relay.
	}

	var input map[string]any
	if err := json.Unmarshal(data, &input); err != nil {
		return nil // Invalid JSON — silently skip.
	}

	eventName, _ := input["hook_event_name"].(string)
	if eventName == "" {
		return nil
	}

	// Build the relay event.
	agentName := resolveAgentName()
	sessionID, _ := input["session_id"].(string)
	cwd, _ := input["cwd"].(string)

	evt := hookRelayEvent{
		Agent:     agentName,
		SessionID: sessionID,
		Event:     eventName,
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		CWD:       cwd,
	}

	// Extract event-specific fields.
	switch eventName {
	case "PreToolUse":
		evt.ToolName, _ = input["tool_name"].(string)
		evt.ToolInput = truncateAny(input["tool_input"], 1024)
	case "PostToolUse":
		evt.ToolName, _ = input["tool_name"].(string)
		evt.ToolInput = truncateAny(input["tool_input"], 1024)
		evt.ToolResponse = truncateAny(input["tool_response"], 1024)
	case "SessionStart":
		evt.Source, _ = input["source"].(string)
	case "SessionEnd":
		evt.Reason, _ = input["reason"].(string)
	case "SubagentStart":
		evt.SubagentID, _ = input["agent_id"].(string)
		evt.SubagentType, _ = input["agent_type"].(string)
	case "SubagentStop":
		evt.SubagentID, _ = input["agent_id"].(string)
		evt.SubagentType, _ = input["agent_type"].(string)
	case "PreCompact":
		evt.Trigger, _ = input["trigger"].(string)
	case "Stop":
		// No additional fields.
	default:
		return nil // Unknown event — skip.
	}

	// Build NATS subject: hooks.<agent>.<event>
	subject := fmt.Sprintf("hooks.%s.%s", sanitizeSubject(agentName), eventName)

	payload, err := json.Marshal(evt)
	if err != nil {
		return nil
	}

	return publishToNATS(natsURL, subject, payload)
}

// resolveAgentName extracts the agent name from environment.
func resolveAgentName() string {
	// BOAT_AGENT_NAME is set by the controller.
	if name := os.Getenv("BOAT_AGENT_NAME"); name != "" {
		return name
	}
	// Fall back to HOSTNAME, stripping the pod prefix.
	hostname, _ := os.Hostname()
	// Pod names are like "crew-gasboat-crew-worker-1" — extract "worker-1".
	parts := strings.Split(hostname, "-")
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "-")
	}
	return hostname
}

// sanitizeSubject replaces characters invalid in NATS subjects.
func sanitizeSubject(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}

// truncateAny JSON-encodes the value and truncates to maxBytes,
// returning nil if the value is nil or empty.
func truncateAny(v any, maxBytes int) any {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	if len(data) <= maxBytes {
		return v
	}
	// Truncate: re-parse the first maxBytes as a string summary.
	return string(data[:maxBytes]) + "..."
}

// publishToNATS connects to NATS, publishes the message, and disconnects.
// Uses a short timeout to stay within the <50ms hook budget.
func publishToNATS(natsURL, subject string, payload []byte) error {
	opts := []nats.Option{
		nats.Name("gb-hook-relay"),
		nats.Timeout(2 * time.Second),
	}

	// Use NATS token if available.
	if token := os.Getenv("COOP_NATS_TOKEN"); token != "" {
		opts = append(opts, nats.Token(token))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return fmt.Errorf("nats connect: %w", err)
	}
	defer nc.Close()

	if err := nc.Publish(subject, payload); err != nil {
		return fmt.Errorf("nats publish: %w", err)
	}

	// Flush to ensure the message is sent before we disconnect.
	return nc.Flush()
}
