package main

import (
	"context"
	"fmt"
	"os"

	"gasboat/controller/internal/beadsapi"
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

// resolveAgentByActor looks up an open agent bead by the actor's assignee name.
// Returns empty string if not found or on error.
func resolveAgentByActor(ctx context.Context, actorName string) string {
	if actorName == "" || actorName == "unknown" {
		return ""
	}
	result, err := daemon.ListBeadsFiltered(ctx, beadsapi.ListBeadsQuery{
		Types:    []string{"agent"},
		Assignee: actorName,
		Statuses: []string{"open", "in_progress"},
		Sort:     "-created_at",
		Limit:    1,
	})
	if err != nil || len(result.Beads) == 0 {
		return ""
	}
	return result.Beads[0].ID
}

// resolveAgentIDWithFallback resolves agent bead ID with actor fallback.
// Priority: flagValue > KD_AGENT_ID env > actor-based lookup.
func resolveAgentIDWithFallback(ctx context.Context, flagValue string) (string, error) {
	id, err := resolveAgentID(flagValue)
	if err == nil {
		return id, nil
	}
	agentID := resolveAgentByActor(ctx, actor)
	if agentID == "" {
		return "", fmt.Errorf("no agent identity found (set KD_AGENT_ID, use --agent-id, or set KD_ACTOR)")
	}
	return agentID, nil
}
