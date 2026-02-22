// Package bridge provides the decision lifecycle watcher.
//
// Decisions subscribes to plain NATS subjects for bead create/close events,
// filters for type=decision beads, and:
//   - On create: notifies an optional Notifier (e.g., Slack).
//   - On close: reads the agent field, looks up the agent's coop_url,
//     and POSTs a nudge so the idle agent wakes up and reads the result.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"gasboat/controller/internal/beadsapi"
)

// BeadClient is the subset of beadsapi.Client used by the bridge package.
type BeadClient interface {
	GetBead(ctx context.Context, beadID string) (*beadsapi.BeadDetail, error)
	CloseBead(ctx context.Context, beadID string, fields map[string]string) error
}

// BeadEvent is the JSON payload published on beads.bead.created / beads.bead.closed.
type BeadEvent struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Title    string            `json:"title"`
	Status   string            `json:"status"`
	Assignee string            `json:"assignee"`
	Labels   []string          `json:"labels"`
	Fields   map[string]string `json:"fields"`
	Priority int               `json:"priority"`
}

// Notifier sends decision lifecycle notifications to an external system.
type Notifier interface {
	// NotifyDecision is called when a new decision bead is created.
	NotifyDecision(ctx context.Context, bead BeadEvent) error
	// UpdateDecision is called when a decision bead is closed/resolved.
	UpdateDecision(ctx context.Context, beadID, chosen string) error
}

// Decisions watches NATS for decision bead lifecycle events.
type Decisions struct {
	natsURL  string
	natsOpts []nats.Option
	daemon   BeadClient
	notifier Notifier // nil = no notifications
	logger   *slog.Logger

	mu   sync.Mutex
	conn *nats.Conn
}

// DecisionsConfig holds configuration for the Decisions watcher.
type DecisionsConfig struct {
	NatsURL   string
	NatsToken string
	Daemon    BeadClient
	Notifier  Notifier
	Logger    *slog.Logger
}

// NewDecisions creates a new decision lifecycle watcher.
func NewDecisions(cfg DecisionsConfig) *Decisions {
	opts := []nats.Option{nats.Name("gasboat-decisions")}
	if cfg.NatsToken != "" {
		opts = append(opts, nats.Token(cfg.NatsToken))
	}
	return &Decisions{
		natsURL:  cfg.NatsURL,
		natsOpts: opts,
		daemon:   cfg.Daemon,
		notifier: cfg.Notifier,
		logger:   cfg.Logger,
	}
}

// Start connects to NATS and subscribes to decision bead events.
// Blocks until ctx is canceled. Reconnects on error.
func (d *Decisions) Start(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := d.run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d.logger.Warn("decisions NATS subscription error, reconnecting",
			"error", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (d *Decisions) run(ctx context.Context) error {
	nc, err := nats.Connect(d.natsURL, d.natsOpts...)
	if err != nil {
		return fmt.Errorf("NATS connect: %w", err)
	}
	defer nc.Close()

	d.mu.Lock()
	d.conn = nc
	d.mu.Unlock()

	// Subscribe to bead lifecycle subjects (plain NATS, not JetStream).
	created, err := nc.Subscribe("beads.bead.created", func(msg *nats.Msg) {
		d.handleCreated(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("subscribe beads.bead.created: %w", err)
	}
	defer func() { _ = created.Unsubscribe() }()

	closed, err := nc.Subscribe("beads.bead.closed", func(msg *nats.Msg) {
		d.handleClosed(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("subscribe beads.bead.closed: %w", err)
	}
	defer func() { _ = closed.Unsubscribe() }()

	d.logger.Info("decisions watcher subscribed to NATS",
		"url", d.natsURL,
		"subjects", []string{"beads.bead.created", "beads.bead.closed"})

	<-ctx.Done()
	return ctx.Err()
}

func (d *Decisions) handleCreated(ctx context.Context, msg *nats.Msg) {
	var bead BeadEvent
	if err := json.Unmarshal(msg.Data, &bead); err != nil {
		d.logger.Debug("skipping malformed bead created event", "error", err)
		return
	}
	if bead.Type != "decision" {
		return
	}

	d.logger.Info("decision bead created",
		"id", bead.ID,
		"title", bead.Title,
		"assignee", bead.Assignee)

	if d.notifier != nil {
		if err := d.notifier.NotifyDecision(ctx, bead); err != nil {
			d.logger.Error("failed to notify decision", "id", bead.ID, "error", err)
		}
	}
}

func (d *Decisions) handleClosed(ctx context.Context, msg *nats.Msg) {
	var bead BeadEvent
	if err := json.Unmarshal(msg.Data, &bead); err != nil {
		d.logger.Debug("skipping malformed bead closed event", "error", err)
		return
	}
	if bead.Type != "decision" {
		return
	}

	chosen := bead.Fields["chosen"]
	d.logger.Info("decision bead closed",
		"id", bead.ID,
		"chosen", chosen,
		"assignee", bead.Assignee)

	// Notify external system (e.g., update Slack message).
	if d.notifier != nil {
		if err := d.notifier.UpdateDecision(ctx, bead.ID, chosen); err != nil {
			d.logger.Error("failed to update decision notification", "id", bead.ID, "error", err)
		}
	}

	// Nudge the agent via coop so it wakes up and reads the decision result.
	d.nudgeAgent(ctx, bead)
}

// nudgeAgent looks up the agent's coop_url from the agent bead and POSTs a nudge.
func (d *Decisions) nudgeAgent(ctx context.Context, bead BeadEvent) {
	agentName := bead.Assignee
	if agentName == "" {
		d.logger.Warn("decision bead has no assignee, cannot nudge", "id", bead.ID)
		return
	}

	// Look up the agent bead to get the coop_url.
	agentBead, err := d.daemon.GetBead(ctx, agentName)
	if err != nil {
		d.logger.Error("failed to get agent bead for nudge",
			"agent", agentName, "decision", bead.ID, "error", err)
		return
	}

	coopURL := agentBead.Fields["coop_url"]
	if coopURL == "" {
		d.logger.Warn("agent bead has no coop_url, cannot nudge",
			"agent", agentName, "decision", bead.ID)
		return
	}

	chosen := bead.Fields["chosen"]
	rationale := bead.Fields["rationale"]
	message := fmt.Sprintf("Decision resolved: %s", chosen)
	if rationale != "" {
		message += fmt.Sprintf(" — %s", rationale)
	}

	if err := nudgeCoop(ctx, coopURL, message); err != nil {
		d.logger.Error("failed to nudge agent",
			"agent", agentName, "coop_url", coopURL, "error", err)
		return
	}

	d.logger.Info("nudged agent after decision resolved",
		"agent", agentName, "decision", bead.ID, "chosen", chosen)
}

// nudgeCoop POSTs a nudge message to a coop agent endpoint.
func nudgeCoop(ctx context.Context, coopURL, message string) error {
	body, err := json.Marshal(map[string]string{"message": message})
	if err != nil {
		return fmt.Errorf("marshal nudge body: %w", err)
	}

	url := coopURL + "/api/v1/agent/nudge"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create nudge request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("nudge request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("nudge returned status %d", resp.StatusCode)
	}
	return nil
}
