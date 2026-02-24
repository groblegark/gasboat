// Package bridge provides the mail lifecycle watcher.
//
// Mail subscribes to kbeads SSE event stream for bead create events,
// filters for type=mail beads, and nudges agents when a message
// requires immediate attention (delivery:interrupt label or high priority).
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gasboat/controller/internal/beadsapi"
)

// noteValue parses "key: value" lines from a notes string and returns the value
// for the given key, or "" if not found.
func noteValue(notes, key string) string {
	for _, line := range strings.Split(notes, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == key {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

// MailConfig holds configuration for the Mail watcher.
type MailConfig struct {
	Daemon BeadClient
	Logger *slog.Logger
}

// Mail watches the kbeads SSE event stream for mail bead lifecycle events.
type Mail struct {
	daemon BeadClient
	logger *slog.Logger
}

// NewMail creates a new mail lifecycle watcher.
func NewMail(cfg MailConfig) *Mail {
	return &Mail{
		daemon: cfg.Daemon,
		logger: cfg.Logger,
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// mail bead created events.
func (m *Mail) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.created", m.handleCreated)
	m.logger.Info("mail watcher registered SSE handlers",
		"topics", []string{"beads.bead.created"})
}

func (m *Mail) handleCreated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		m.logger.Debug("skipping malformed bead created event")
		return
	}
	if bead.Type != "mail" {
		return
	}

	m.logger.Info("mail bead created",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee,
		"priority", bead.Priority)

	// Determine if the agent should be nudged immediately.
	if !m.shouldNudge(*bead) {
		return
	}

	m.nudgeAgent(ctx, *bead)
}

// shouldNudge returns true if the mail bead warrants an immediate agent nudge.
// Nudge when delivery:interrupt label is present OR priority <= 1 (critical/high).
func (m *Mail) shouldNudge(bead BeadEvent) bool {
	for _, label := range bead.Labels {
		if label == "delivery:interrupt" {
			return true
		}
	}
	// Priority 0 = critical, 1 = high → nudge.
	// Priority 2 = normal, 3 = low → let periodic hooks handle it.
	return bead.Priority <= 1
}

// nudgeAgent looks up the agent's coop_url and POSTs a nudge.
func (m *Mail) nudgeAgent(ctx context.Context, bead BeadEvent) {
	agentName := bead.Assignee
	if agentName == "" {
		m.logger.Warn("mail bead has no assignee, cannot nudge", "id", bead.ID)
		return
	}

	// Find the agent bead by assignee name (not by ID).
	agentBead, err := m.daemon.FindAgentBead(ctx, agentName)
	if err != nil {
		m.logger.Error("failed to find agent bead for mail nudge",
			"agent", agentName, "mail", bead.ID, "error", err)
		return
	}
	if agentBead == nil {
		m.logger.Warn("no agent bead found for assignee, cannot nudge",
			"agent", agentName, "mail", bead.ID)
		return
	}

	// coop_url is stored in the notes field as "key: value" lines, not in fields.
	coopURL := noteValue(agentBead.Notes, "coop_url")
	if coopURL == "" {
		m.logger.Warn("agent bead has no coop_url in notes, cannot nudge",
			"agent", agentName, "mail", bead.ID)
		return
	}

	// Build sender info from labels.
	sender := "unknown"
	for _, label := range bead.Labels {
		if strings.HasPrefix(label, "from:") {
			sender = strings.TrimPrefix(label, "from:")
			break
		}
	}

	message := fmt.Sprintf("New mail from %s: %s — run 'kd show %s' to read", sender, bead.Title, bead.ID)

	if err := nudgeCoop(ctx, coopURL, message); err != nil {
		m.logger.Error("failed to nudge agent for mail",
			"agent", agentName, "coop_url", coopURL, "error", err)
		return
	}

	m.logger.Info("nudged agent for urgent mail",
		"agent", agentName, "mail", bead.ID, "sender", sender)
}
