// Package client queries the beads daemon for bead state via gRPC.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	beadsv1 "github.com/alfredjeanlab/beads/gen/beads/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// AgentBead represents an active agent bead from the daemon.
type AgentBead struct {
	// ID is the bead identifier (e.g., "crew-town-crew-hq", "crew-gasboat-crew-k8s").
	ID string

	// Project is the project name (e.g., "town", "gasboat").
	Project string

	// Mode is the agent mode (e.g., "crew", "job").
	Mode string

	// Role is the agent role (e.g., "mate", "deckhand", "ops").
	Role string

	// AgentName is the agent's name within its role (e.g., "hq", "k8s").
	AgentName string

	// Metadata contains additional bead metadata from the daemon.
	Metadata map[string]string
}

// BeadLister lists active agent beads from the daemon.
type BeadLister interface {
	ListAgentBeads(ctx context.Context) ([]AgentBead, error)
}

// Config for the daemon gRPC client.
type Config struct {
	// GRPCAddr is the daemon gRPC address (e.g., "daemon:9090").
	GRPCAddr string
}

// Client queries the beads daemon via gRPC.
type Client struct {
	conn  *grpc.ClientConn
	beads beadsv1.BeadsServiceClient
}

// New creates a gRPC client for querying the beads daemon.
func New(cfg Config) (*Client, error) {
	conn, err := grpc.NewClient(cfg.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dialing beads daemon at %s: %w", cfg.GRPCAddr, err)
	}
	return &Client{
		conn:  conn,
		beads: beadsv1.NewBeadsServiceClient(conn),
	}, nil
}

// Close releases the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// activeStatuses is the set of statuses that represent non-closed beads.
var activeStatuses = []string{"open", "in_progress", "blocked", "deferred"}

// ListAgentBeads queries the daemon for active agent beads (type=agent).
func (c *Client) ListAgentBeads(ctx context.Context) ([]AgentBead, error) {
	resp, err := c.beads.ListBeads(ctx, &beadsv1.ListBeadsRequest{
		Type:   []string{"agent"},
		Status: activeStatuses,
	})
	if err != nil {
		return nil, fmt.Errorf("listing agent beads: %w", err)
	}

	var beads []AgentBead
	for _, b := range resp.GetBeads() {
		fields := parseFieldsJSON(b.GetFields())
		project := fields["project"]
		mode := fields["mode"]
		role := fields["role"]
		name := fields["agent"]
		if mode == "" {
			mode = "crew"
		}
		if role == "" || name == "" {
			continue
		}
		beads = append(beads, AgentBead{
			ID:        b.GetId(),
			Project:   project,
			Mode:      mode,
			Role:      role,
			AgentName: name,
			Metadata:  parseNotes(b.GetNotes()),
		})
	}

	return beads, nil
}

// ProjectInfo represents a registered project from daemon project beads.
type ProjectInfo struct {
	Name          string // Project name (from bead title)
	Prefix        string // Beads prefix (e.g., "bd", "bot")
	GitURL        string // Repository URL
	DefaultBranch string // Default branch (e.g., "main")
	Image         string // Per-project agent image override
	StorageClass  string // Per-project PVC storage class override
}

// ListProjectBeads queries the daemon for project beads (type=project) and extracts
// project metadata from fields. Returns a map of project name → ProjectInfo.
func (c *Client) ListProjectBeads(ctx context.Context) (map[string]ProjectInfo, error) {
	resp, err := c.beads.ListBeads(ctx, &beadsv1.ListBeadsRequest{
		Type:   []string{"project"},
		Status: activeStatuses,
	})
	if err != nil {
		return nil, fmt.Errorf("listing project beads: %w", err)
	}

	rigs := make(map[string]ProjectInfo)
	for _, b := range resp.GetBeads() {
		// Strip "Project: " prefix from title — legacy project beads may have titles
		// like "Project: beads" instead of just "beads".
		name := strings.TrimPrefix(b.GetTitle(), "Project: ")
		fields := parseFieldsJSON(b.GetFields())
		info := ProjectInfo{
			Name:          name,
			Prefix:        fields["prefix"],
			GitURL:        fields["git_url"],
			DefaultBranch: fields["default_branch"],
			Image:         fields["image"],
			StorageClass:  fields["storage_class"],
		}
		if name != "" {
			rigs[name] = info
		}
	}

	return rigs, nil
}

// BeadDetail represents a full bead returned by the daemon.
type BeadDetail struct {
	ID     string            `json:"id"`
	Title  string            `json:"title"`
	Type   string            `json:"type"`
	Status string            `json:"status"`
	Labels []string          `json:"labels"`
	Notes  string            `json:"notes"`
	Fields map[string]string `json:"fields"`
}

// beadToDetail converts a proto Bead to a BeadDetail.
func beadToDetail(b *beadsv1.Bead) *BeadDetail {
	return &BeadDetail{
		ID:     b.GetId(),
		Title:  b.GetTitle(),
		Type:   b.GetType(),
		Status: b.GetStatus(),
		Labels: b.GetLabels(),
		Notes:  b.GetNotes(),
		Fields: parseFieldsJSON(b.GetFields()),
	}
}

// GetBead fetches a single bead by ID from the daemon.
func (c *Client) GetBead(ctx context.Context, beadID string) (*BeadDetail, error) {
	resp, err := c.beads.GetBead(ctx, &beadsv1.GetBeadRequest{Id: beadID})
	if err != nil {
		return nil, fmt.Errorf("getting bead %s: %w", beadID, err)
	}
	return beadToDetail(resp.GetBead()), nil
}

// UpdateBeadFields updates typed fields on a bead via a read-modify-write cycle.
// The daemon replaces the full fields JSON, so we must merge with existing fields.
func (c *Client) UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error {
	// Read current fields.
	resp, err := c.beads.GetBead(ctx, &beadsv1.GetBeadRequest{Id: beadID})
	if err != nil {
		return fmt.Errorf("reading bead %s for field update: %w", beadID, err)
	}

	// Merge new fields into existing.
	existing := parseFieldsJSON(resp.GetBead().GetFields())
	for k, v := range fields {
		existing[k] = v
	}

	merged, err := marshalFields(existing)
	if err != nil {
		return fmt.Errorf("marshalling merged fields for %s: %w", beadID, err)
	}

	_, err = c.beads.UpdateBead(ctx, &beadsv1.UpdateBeadRequest{
		Id:     beadID,
		Fields: merged,
	})
	if err != nil {
		return fmt.Errorf("updating fields on bead %s: %w", beadID, err)
	}
	return nil
}

// UpdateBeadNotes updates the notes field of a bead.
func (c *Client) UpdateBeadNotes(ctx context.Context, beadID, notes string) error {
	_, err := c.beads.UpdateBead(ctx, &beadsv1.UpdateBeadRequest{
		Id:    beadID,
		Notes: &notes,
	})
	if err != nil {
		return fmt.Errorf("updating notes on bead %s: %w", beadID, err)
	}
	return nil
}

// UpdateAgentState updates the agent_state field of a bead.
func (c *Client) UpdateAgentState(ctx context.Context, beadID, state string) error {
	return c.UpdateBeadFields(ctx, beadID, map[string]string{"agent_state": state})
}

// CloseBead closes a bead by ID, optionally setting fields before closing.
func (c *Client) CloseBead(ctx context.Context, beadID string, fields map[string]string) error {
	if len(fields) > 0 {
		if err := c.UpdateBeadFields(ctx, beadID, fields); err != nil {
			return fmt.Errorf("updating fields before close: %w", err)
		}
	}

	_, err := c.beads.CloseBead(ctx, &beadsv1.CloseBeadRequest{
		Id:       beadID,
		ClosedBy: "gasboat",
	})
	if err != nil {
		return fmt.Errorf("closing bead %s: %w", beadID, err)
	}
	return nil
}

// SetConfig upserts a config key/value in the daemon.
func (c *Client) SetConfig(ctx context.Context, key string, value []byte) error {
	_, err := c.beads.SetConfig(ctx, &beadsv1.SetConfigRequest{
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("setting config %s: %w", key, err)
	}
	return nil
}

// parseFieldsJSON decodes the proto fields []byte (JSON object) into a string map.
// Returns an empty map on nil/empty input or decode error.
func parseFieldsJSON(data []byte) map[string]string {
	if len(data) == 0 {
		return make(map[string]string)
	}
	m := make(map[string]string)
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string]string)
	}
	return m
}

// marshalFields encodes a string map to JSON bytes for the proto fields []byte.
func marshalFields(m map[string]string) ([]byte, error) {
	return json.Marshal(m)
}

// parseNotes parses "key: value" lines from a bead's notes field into a map.
func parseNotes(notes string) map[string]string {
	if notes == "" {
		return nil
	}
	m := make(map[string]string)
	for _, line := range strings.Split(notes, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			m[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
