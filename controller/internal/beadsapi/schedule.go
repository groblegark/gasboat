package beadsapi

import (
	"context"
	"fmt"
)

// ScheduleBead represents a schedule config bead that defines a recurring
// agent spawn. The controller's scheduler watches these and fires cron jobs.
type ScheduleBead struct {
	ID       string // Bead ID
	Title    string // Human-readable name (e.g., "Daily Release Notes")
	Project  string // Project for spawned agent
	Cron     string // Cron expression (e.g., "0 0 * * *")
	Role     string // Agent role (default: "crew")
	Prompt   string // Prompt injected into the agent
	Enabled  bool   // Whether the schedule is active
	Timezone string // Timezone name (default: "UTC")
	LastRun  string // ISO timestamp of last successful spawn
}

// ListScheduleBeads queries the daemon for active schedule beads (type=schedule)
// and returns them as ScheduleBead structs.
func (c *Client) ListScheduleBeads(ctx context.Context) ([]ScheduleBead, error) {
	resp, err := c.listBeads(ctx, []string{"schedule"}, activeStatuses)
	if err != nil {
		return nil, fmt.Errorf("listing schedule beads: %w", err)
	}

	var schedules []ScheduleBead
	for _, b := range resp.Beads {
		fields := b.fieldsMap()
		cron := fields["cron"]
		if cron == "" {
			continue // cron is required
		}
		enabled := fields["enabled"]
		// Default to enabled when the field is empty or "true".
		isEnabled := enabled == "" || enabled == "true"

		role := fields["role"]
		if role == "" {
			role = "crew"
		}

		schedules = append(schedules, ScheduleBead{
			ID:       b.ID,
			Title:    b.Title,
			Project:  fields["project"],
			Cron:     cron,
			Role:     role,
			Prompt:   fields["prompt"],
			Enabled:  isEnabled,
			Timezone: fields["timezone"],
			LastRun:  fields["last_run"],
		})
	}
	return schedules, nil
}
