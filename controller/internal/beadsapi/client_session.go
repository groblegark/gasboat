package beadsapi

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SessionFields contains metadata for a session bead.
type SessionFields struct {
	Agent       string `json:"agent"`
	AgentBeadID string `json:"agent_bead_id"`
	Project     string `json:"project"`
	Role        string `json:"role"`
	Hostname    string `json:"hostname"`
	SessionLog  string `json:"session_log,omitempty"`
	StartedAt   string `json:"started_at"`
	EndedAt     string `json:"ended_at,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Resumed     bool   `json:"resumed"`
	Status      string `json:"status"` // active, completed, crashed, retired
}

// RegisterSession creates a new session bead linked to an agent bead.
// Returns the session bead ID.
func (c *Client) RegisterSession(ctx context.Context, fields SessionFields) (string, error) {
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return "", fmt.Errorf("marshalling session fields: %w", err)
	}

	title := fmt.Sprintf("session:%s:%s", fields.Agent, fields.StartedAt)

	req := CreateBeadRequest{
		Title:  title,
		Type:   "session",
		Kind:   "config",
		Fields: json.RawMessage(fieldsJSON),
	}
	if fields.Project != "" {
		req.Labels = []string{"project:" + fields.Project}
	}

	id, err := c.CreateBead(ctx, req)
	if err != nil {
		return "", fmt.Errorf("creating session bead: %w", err)
	}

	// Link session to agent bead.
	if fields.AgentBeadID != "" {
		_ = c.AddDependency(ctx, id, fields.AgentBeadID, "session_of", fields.Agent)
	}

	return id, nil
}

// CloseSession closes a session bead with final metadata.
func (c *Client) CloseSession(ctx context.Context, sessionBeadID string, exitCode int, sessionLog string) error {
	fields := map[string]any{
		"ended_at":  time.Now().UTC().Format(time.RFC3339),
		"exit_code": exitCode,
		"status":    "completed",
	}
	if exitCode != 0 {
		fields["status"] = "crashed"
	}
	if sessionLog != "" {
		fields["session_log"] = sessionLog
	}

	if err := c.UpdateBeadFieldsTyped(ctx, sessionBeadID, fields); err != nil {
		return fmt.Errorf("updating session bead %s: %w", sessionBeadID, err)
	}
	return c.CloseBead(ctx, sessionBeadID, nil)
}

// ListSessionBeads returns session beads, optionally filtered by agent name.
func (c *Client) ListSessionBeads(ctx context.Context, agentName string, limit int) ([]*BeadDetail, error) {
	q := ListBeadsQuery{
		Types: []string{"session"},
		Sort:  "-created_at",
		Limit: limit,
	}
	if agentName != "" {
		q.Search = agentName
	}

	result, err := c.ListBeadsFiltered(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("listing session beads: %w", err)
	}
	return result.Beads, nil
}
