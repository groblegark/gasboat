package main

import (
	"context"
	"fmt"
	"os"
)

// resolveAgentID returns the agent identity for the current session.
// Priority: KD_AGENT_ID env > error.
//
// In K8s agent pods, KD_AGENT_ID is always set by the controller (via podspec).
// For local development, the user must set it explicitly or use --agent-id.
func resolveAgentID(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if id := os.Getenv("KD_AGENT_ID"); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("agent ID not set: use --agent-id or set KD_AGENT_ID")
}

// resolveSessionID looks up the session_id field on the agent bead.
// This is the session identity used for gates and decisions.
func resolveSessionID(ctx context.Context, agentBeadID string) (string, error) {
	bead, err := daemon.GetBead(ctx, agentBeadID)
	if err != nil {
		return "", fmt.Errorf("resolving session ID for %s: %w", agentBeadID, err)
	}
	if sid, ok := bead.Fields["session_id"]; ok && sid != "" {
		return sid, nil
	}
	// Fall back to agent bead ID as session ID.
	return agentBeadID, nil
}
