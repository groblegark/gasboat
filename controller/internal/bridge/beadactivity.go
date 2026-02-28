// Package bridge provides the bead activity watcher.
//
// BeadActivity subscribes to kbeads SSE event stream for bead lifecycle events
// and posts notifications in agent Slack threads when agents create, claim, or
// close beads. This gives operators real-time visibility into agent activity
// without checking kd list.
//
// Only work-item bead types (task, bug, feature, epic) are tracked. Infrastructure
// beads (agent, decision, mail, project, etc.) are excluded to avoid noise.
package bridge

import (
	"context"
	"log/slog"
	"sync"
)

// workItemTypes is the set of bead types that represent real work items.
// Infrastructure types (agent, decision, mail, project, etc.) are excluded.
var workItemTypes = map[string]bool{
	"task":    true,
	"bug":     true,
	"feature": true,
	"epic":    true,
}

// BeadActivityNotifier posts bead lifecycle notifications to agent Slack threads.
type BeadActivityNotifier interface {
	// NotifyBeadCreated is called when an agent creates a work-item bead.
	NotifyBeadCreated(ctx context.Context, bead BeadEvent)
	// NotifyBeadClaimed is called when an agent claims a bead (status → in_progress).
	NotifyBeadClaimed(ctx context.Context, bead BeadEvent)
	// NotifyBeadClosed is called when a bead assigned to an agent is closed.
	NotifyBeadClosed(ctx context.Context, bead BeadEvent)
}

// BeadActivityConfig holds configuration for the BeadActivity watcher.
type BeadActivityConfig struct {
	Notifier BeadActivityNotifier // nil = no notifications
	Logger   *slog.Logger
}

// BeadActivity watches the kbeads SSE event stream for bead lifecycle events
// and dispatches notifications to agent Slack threads.
type BeadActivity struct {
	notifier BeadActivityNotifier
	logger   *slog.Logger

	mu   sync.Mutex
	seen map[string]bool // bead ID:action → already notified (dedup on SSE reconnect)
}

// NewBeadActivity creates a new bead activity watcher.
func NewBeadActivity(cfg BeadActivityConfig) *BeadActivity {
	return &BeadActivity{
		notifier: cfg.Notifier,
		logger:   cfg.Logger,
		seen:     make(map[string]bool),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream.
func (ba *BeadActivity) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.created", ba.handleCreated)
	stream.On("beads.bead.closed", ba.handleClosed)
	stream.On("beads.bead.updated", ba.handleUpdated)
	ba.logger.Info("bead activity watcher registered SSE handlers",
		"topics", []string{"beads.bead.created", "beads.bead.closed", "beads.bead.updated"})
}

func (ba *BeadActivity) handleCreated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || !workItemTypes[bead.Type] {
		return
	}
	if bead.CreatedBy == "" {
		return
	}

	if ba.alreadySeen(bead.ID, "created") {
		return
	}

	ba.logger.Debug("bead created by agent",
		"id", bead.ID, "type", bead.Type, "title", bead.Title, "created_by", bead.CreatedBy)

	if ba.notifier != nil {
		ba.notifier.NotifyBeadCreated(ctx, *bead)
	}
}

func (ba *BeadActivity) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || !workItemTypes[bead.Type] {
		return
	}
	if bead.Assignee == "" {
		return
	}

	if ba.alreadySeen(bead.ID, "closed") {
		return
	}

	ba.logger.Debug("bead closed for agent",
		"id", bead.ID, "type", bead.Type, "title", bead.Title, "assignee", bead.Assignee)

	if ba.notifier != nil {
		ba.notifier.NotifyBeadClosed(ctx, *bead)
	}
}

func (ba *BeadActivity) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil || !workItemTypes[bead.Type] {
		return
	}

	// Detect claim: bead transitions to in_progress with an assignee.
	if bead.Status == "in_progress" && bead.Assignee != "" {
		if ba.alreadySeen(bead.ID, "claimed") {
			return
		}

		ba.logger.Debug("bead claimed by agent",
			"id", bead.ID, "type", bead.Type, "title", bead.Title, "assignee", bead.Assignee)

		if ba.notifier != nil {
			ba.notifier.NotifyBeadClaimed(ctx, *bead)
		}
	}
}

// alreadySeen returns true if this (beadID, action) pair has already been
// notified, and records it if not. Prevents duplicates on SSE reconnect.
func (ba *BeadActivity) alreadySeen(beadID, action string) bool {
	key := beadID + ":" + action
	ba.mu.Lock()
	defer ba.mu.Unlock()
	if ba.seen[key] {
		return true
	}
	ba.seen[key] = true
	return false
}
