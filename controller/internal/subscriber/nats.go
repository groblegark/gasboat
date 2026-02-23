// Package subscriber watches for agent lifecycle events via NATS JetStream
// and emits them on a channel. The controller's main loop reads these events
// and translates them to K8s pod operations.
package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// EventType identifies the kind of beads lifecycle event.
type EventType string

const (
	// AgentSpawn means a new agent needs a pod (agent bead created).
	AgentSpawn EventType = "agent_spawn"

	// AgentDone means an agent completed its work (bot done, bead closed).
	AgentDone EventType = "agent_done"

	// AgentStuck means an agent is unresponsive (escalation).
	AgentStuck EventType = "agent_stuck"

	// AgentKill means an agent should be terminated (lifecycle shutdown).
	AgentKill EventType = "agent_kill"

	// AgentUpdate means agent bead metadata was changed (e.g., sidecar profile).
	AgentUpdate EventType = "agent_update"

	// AgentStop means an agent should be gracefully stopped (agent_state=stopping).
	AgentStop EventType = "agent_stop"
)

// Event represents a beads lifecycle event that requires a pod operation.
type Event struct {
	Type      EventType
	Project   string
	Mode      string
	Role      string // functional role from role bead (e.g., "devops", "qa")
	AgentName string
	BeadID    string            // The bead that triggered this event
	Metadata  map[string]string // Additional context from beads
}

// Watcher subscribes to BD Daemon lifecycle events and emits them on a channel.
type Watcher interface {
	// Start begins watching for beads events. Blocks until ctx is canceled.
	Start(ctx context.Context) error

	// Events returns a read-only channel of lifecycle events.
	Events() <-chan Event
}

// Config holds configuration for the watcher.
type Config struct {
	// NatsURL is the NATS server URL (e.g., "nats://host:4222").
	NatsURL string

	// NatsToken is the auth token for NATS (optional).
	NatsToken string

	// ConsumerName is the durable consumer name for JetStream.
	// Allows crash recovery and fan-out across replicas.
	ConsumerName string

	// Namespace is the default K8s namespace for pod metadata.
	Namespace string

	// CoopImage is the default container image for agent pods.
	CoopImage string

	// BeadsGRPCAddr is the beads daemon gRPC address (host:port) for agent pod env vars.
	BeadsGRPCAddr string
}

// beadEventPayload is the event structure published by the beads daemon.
// The daemon publishes to subjects like beads.bead.created, beads.bead.updated, etc.
type beadEventPayload struct {
	Bead    beadData       `json:"bead"`
	Changes map[string]any `json:"changes,omitempty"`
}

// beadData mirrors the bead fields from the daemon's event payload.
type beadData struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Type       string            `json:"type"`
	Status     string            `json:"status"`
	Labels     []string          `json:"labels"`
	Fields     map[string]string `json:"fields"`
	AgentState string            `json:"agent_state"`
	Assignee   string            `json:"assignee"`
	CreatedBy  string            `json:"created_by"`
}

// NATSWatcher subscribes to the MUTATION_EVENTS JetStream stream and translates
// bead events into lifecycle Events. Uses a durable consumer for crash recovery
// and replay.
type NATSWatcher struct {
	cfg    Config
	events chan Event
	logger *slog.Logger
}

// NewNATSWatcher creates a watcher backed by JetStream bead events.
func NewNATSWatcher(cfg Config, logger *slog.Logger) *NATSWatcher {
	return &NATSWatcher{
		cfg:    cfg,
		events: make(chan Event, 64),
		logger: logger,
	}
}

// Start begins watching the JetStream stream. Blocks until ctx is canceled.
// Reconnects with exponential backoff on errors.
func (w *NATSWatcher) Start(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			close(w.events)
			return fmt.Errorf("watcher stopped: %w", ctx.Err())
		default:
		}

		err := w.subscribe(ctx)
		if err != nil {
			if ctx.Err() != nil {
				close(w.events)
				return fmt.Errorf("watcher stopped: %w", ctx.Err())
			}
			w.logger.Warn("JetStream subscription error, reconnecting",
				"error", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				close(w.events)
				return fmt.Errorf("watcher stopped: %w", ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = time.Second
		}
	}
}

// Events returns a read-only channel of lifecycle events.
func (w *NATSWatcher) Events() <-chan Event {
	return w.events
}

// subscribe connects to NATS and subscribes to MUTATION_EVENTS via JetStream.
func (w *NATSWatcher) subscribe(ctx context.Context) error {
	opts := []nats.Option{
		nats.Name("gasboat-controller"),
	}
	if w.cfg.NatsToken != "" {
		opts = append(opts, nats.Token(w.cfg.NatsToken))
	}

	nc, err := nats.Connect(w.cfg.NatsURL, opts...)
	if err != nil {
		return fmt.Errorf("NATS connect: %w", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("JetStream context: %w", err)
	}

	consumerName := w.cfg.ConsumerName
	if consumerName == "" {
		consumerName = "controller"
	}

	w.logger.Info("subscribing to MUTATION_EVENTS stream",
		"consumer", consumerName, "url", w.cfg.NatsURL)

	// Subscribe with a durable pull consumer for reliable delivery.
	// The beads daemon publishes to beads.bead.{created,updated,closed,deleted}.
	sub, err := js.PullSubscribe(
		"beads.>",
		consumerName,
		nats.AckExplicit(),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("JetStream subscribe: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	w.logger.Info("JetStream subscription active")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// Fetch messages in batches with a timeout.
		msgs, err := sub.Fetch(10, nats.MaxWait(2*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue // No messages available, loop back
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("JetStream fetch: %w", err)
		}

		for _, msg := range msgs {
			w.processMessage(msg)
			if err := msg.Ack(); err != nil {
				w.logger.Warn("failed to ack message", "error", err)
			}
		}
	}
}

// processMessage parses a JetStream message and emits a lifecycle Event if relevant.
func (w *NATSWatcher) processMessage(msg *nats.Msg) {
	var payload beadEventPayload
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		w.logger.Debug("skipping malformed JetStream message", "subject", msg.Subject, "error", err)
		return
	}

	if payload.Bead.Type != "agent" {
		return
	}

	// Derive the event type from the NATS subject.
	// Subjects: beads.bead.created, beads.bead.updated, beads.bead.closed, beads.bead.deleted
	eventAction := subjectAction(msg.Subject)

	event, ok := w.mapBeadEvent(eventAction, payload)
	if !ok {
		return
	}

	w.logger.Info("emitting lifecycle event",
		"type", event.Type, "project", event.Project,
		"role", event.Role, "agent", event.AgentName,
		"bead", event.BeadID)

	select {
	case w.events <- event:
	default:
		w.logger.Warn("event channel full, dropping event",
			"type", event.Type, "bead", event.BeadID)
	}
}

// subjectAction extracts the action from a NATS subject.
// e.g., "beads.bead.created" → "created"
func subjectAction(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return ""
}

// mapBeadEvent maps a daemon bead event to a subscriber Event.
func (w *NATSWatcher) mapBeadEvent(action string, payload beadEventPayload) (Event, bool) {
	switch action {
	case "created":
		return w.buildEvent(AgentSpawn, payload.Bead)
	case "updated":
		// Check if agent_state changed to stopping.
		if payload.Bead.AgentState == "stopping" {
			return w.buildEvent(AgentStop, payload.Bead)
		}
		// Check if status changed to in_progress (re-spawn).
		if payload.Bead.Status == "in_progress" {
			if _, ok := payload.Changes["status"]; ok {
				return w.buildEvent(AgentSpawn, payload.Bead)
			}
		}
		return w.buildEvent(AgentUpdate, payload.Bead)
	case "closed":
		return w.buildEvent(AgentDone, payload.Bead)
	case "deleted":
		return w.buildEvent(AgentKill, payload.Bead)
	default:
		return Event{}, false
	}
}

// buildEvent constructs a lifecycle Event from a bead payload.
func (w *NATSWatcher) buildEvent(eventType EventType, bead beadData) (Event, bool) {
	project := bead.Fields["project"]
	mode := bead.Fields["mode"]
	role := bead.Fields["role"]
	name := bead.Fields["agent"]
	if mode == "" {
		mode = "crew"
	}

	if role == "" || name == "" {
		w.logger.Debug("skipping event with incomplete agent info",
			"action", eventType, "bead_id", bead.ID, "title", bead.Title)
		return Event{}, false
	}

	meta := map[string]string{
		"namespace": w.cfg.Namespace,
	}
	if w.cfg.CoopImage != "" {
		meta["image"] = w.cfg.CoopImage
	}
	if w.cfg.BeadsGRPCAddr != "" {
		meta["beads_grpc_addr"] = w.cfg.BeadsGRPCAddr
	}
	if model := bead.Fields["model"]; model != "" {
		meta["model"] = model
	}

	return Event{
		Type:      eventType,
		Project:   project,
		Mode:      mode,
		Role:      role,
		AgentName: name,
		BeadID:    bead.ID,
		Metadata:  meta,
	}, true
}
