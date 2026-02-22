// Package bridge provides the mail lifecycle watcher.
//
// Mail subscribes to plain NATS subjects for bead create events,
// filters for type=mail beads, and nudges agents when a message
// requires immediate attention (delivery:interrupt label or high priority).
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// MailConfig holds configuration for the Mail watcher.
type MailConfig struct {
	NatsURL   string
	NatsToken string
	Daemon    BeadClient
	Logger    *slog.Logger
}

// Mail watches NATS for mail bead lifecycle events.
type Mail struct {
	natsURL  string
	natsOpts []nats.Option
	daemon   BeadClient
	logger   *slog.Logger

	mu   sync.Mutex
	conn *nats.Conn
}

// NewMail creates a new mail lifecycle watcher.
func NewMail(cfg MailConfig) *Mail {
	opts := []nats.Option{nats.Name("gasboat-mail")}
	if cfg.NatsToken != "" {
		opts = append(opts, nats.Token(cfg.NatsToken))
	}
	return &Mail{
		natsURL:  cfg.NatsURL,
		natsOpts: opts,
		daemon:   cfg.Daemon,
		logger:   cfg.Logger,
	}
}

// Start connects to NATS and subscribes to mail bead events.
// Blocks until ctx is canceled. Reconnects on error.
func (m *Mail) Start(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := m.run(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.logger.Warn("mail NATS subscription error, reconnecting",
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

func (m *Mail) run(ctx context.Context) error {
	nc, err := nats.Connect(m.natsURL, m.natsOpts...)
	if err != nil {
		return fmt.Errorf("NATS connect: %w", err)
	}
	defer nc.Close()

	m.mu.Lock()
	m.conn = nc
	m.mu.Unlock()

	// Subscribe to bead created events (plain NATS, not JetStream).
	// Only created matters for mail — closing is "mark read", nothing to wake for.
	created, err := nc.Subscribe("beads.bead.created", func(msg *nats.Msg) {
		m.handleCreated(ctx, msg)
	})
	if err != nil {
		return fmt.Errorf("subscribe beads.bead.created: %w", err)
	}
	defer func() { _ = created.Unsubscribe() }()

	m.logger.Info("mail watcher subscribed to NATS",
		"url", m.natsURL,
		"subject", "beads.bead.created")

	<-ctx.Done()
	return ctx.Err()
}

func (m *Mail) handleCreated(ctx context.Context, msg *nats.Msg) {
	var bead BeadEvent
	if err := json.Unmarshal(msg.Data, &bead); err != nil {
		m.logger.Debug("skipping malformed bead created event", "error", err)
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
	if !m.shouldNudge(bead) {
		return
	}

	m.nudgeAgent(ctx, bead)
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

	agentBead, err := m.daemon.GetBead(ctx, agentName)
	if err != nil {
		m.logger.Error("failed to get agent bead for mail nudge",
			"agent", agentName, "mail", bead.ID, "error", err)
		return
	}

	coopURL := agentBead.Fields["coop_url"]
	if coopURL == "" {
		m.logger.Warn("agent bead has no coop_url, cannot nudge",
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

	message := fmt.Sprintf("New mail from %s: %s — run 'bd show %s' to read", sender, bead.Title, bead.ID)

	if err := nudgeCoop(ctx, coopURL, message); err != nil {
		m.logger.Error("failed to nudge agent for mail",
			"agent", agentName, "coop_url", coopURL, "error", err)
		return
	}

	m.logger.Info("nudged agent for urgent mail",
		"agent", agentName, "mail", bead.ID, "sender", sender)
}
