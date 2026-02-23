// Package bridge provides SSE event stream support for the slack-bridge.
//
// The SSEStream connects to kbeads' Server-Sent Events endpoint and delivers
// parsed bead lifecycle events to registered handlers. It replaces the previous
// NATS subscription-based approach with a direct HTTP/SSE connection.
package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SSEStream connects to the kbeads SSE endpoint and dispatches bead lifecycle
// events to registered topic handlers. It auto-reconnects with exponential
// backoff and uses Last-Event-ID for reconnection replay.
type SSEStream struct {
	baseURL string
	topics  []string
	logger  *slog.Logger

	handlers map[string][]SSEHandler // topic -> handlers
	lastID   string                  // last event ID for reconnection
}

// SSEHandler is a callback for SSE events on a specific topic.
type SSEHandler func(ctx context.Context, data []byte)

// SSEStreamConfig holds configuration for the SSE event stream.
type SSEStreamConfig struct {
	// BeadsHTTPAddr is the kbeads HTTP address (e.g., "http://localhost:8080").
	BeadsHTTPAddr string
	// Topics is the list of topic patterns to subscribe to.
	Topics []string
	// Logger for diagnostic output.
	Logger *slog.Logger
}

// NewSSEStream creates a new SSE event stream for the slack-bridge.
func NewSSEStream(cfg SSEStreamConfig) *SSEStream {
	return &SSEStream{
		baseURL:  strings.TrimRight(cfg.BeadsHTTPAddr, "/"),
		topics:   cfg.Topics,
		logger:   cfg.Logger,
		handlers: make(map[string][]SSEHandler),
	}
}

// On registers a handler for a specific SSE topic (e.g., "beads.bead.created").
func (s *SSEStream) On(topic string, handler SSEHandler) {
	s.handlers[topic] = append(s.handlers[topic], handler)
}

// Start connects to the SSE endpoint and streams events to registered handlers.
// Blocks until ctx is canceled. Reconnects with exponential backoff on errors.
func (s *SSEStream) Start(ctx context.Context) error {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := s.stream(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.logger.Warn("SSE connection lost, reconnecting",
			"error", err, "backoff", backoff, "last_id", s.lastID)
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

// stream opens a single SSE connection and reads events until error or context cancellation.
func (s *SSEStream) stream(ctx context.Context) error {
	url := s.baseURL + "/v1/events/stream"
	if len(s.topics) > 0 {
		url += "?topics=" + strings.Join(s.topics, ",")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if s.lastID != "" {
		req.Header.Set("Last-Event-ID", s.lastID)
	}

	// Long-lived connection -- no timeout.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("SSE endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	s.logger.Info("SSE stream connected",
		"url", url, "last_id", s.lastID)

	return s.readEvents(ctx, resp.Body)
}

// readEvents reads and dispatches SSE events from the response body.
func (s *SSEStream) readEvents(ctx context.Context, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var currentID, currentEvent string
	var currentData strings.Builder

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()

		// Empty line = end of event.
		if line == "" {
			if currentData.Len() > 0 && currentEvent != "" {
				s.dispatch(ctx, currentID, currentEvent, currentData.String())
			}
			if currentID != "" {
				s.lastID = currentID
			}
			currentID = ""
			currentEvent = ""
			currentData.Reset()
			continue
		}

		// Comment lines (keepalive).
		if strings.HasPrefix(line, ":") {
			continue
		}

		// Parse SSE field:value.
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "id":
			currentID = value
		case "event":
			currentEvent = value
		case "data":
			if currentData.Len() > 0 {
				currentData.WriteByte('\n')
			}
			currentData.WriteString(value)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("SSE read: %w", err)
	}
	return fmt.Errorf("SSE stream ended (EOF)")
}

// dispatch calls all registered handlers for the given topic.
func (s *SSEStream) dispatch(ctx context.Context, id, topic, data string) {
	handlers, ok := s.handlers[topic]
	if !ok {
		return
	}
	for _, h := range handlers {
		h(ctx, []byte(data))
	}
}

// sseBeadWrapper is the kbeads SSE payload for bead lifecycle events.
// BeadCreated: {"bead": {...}}
// BeadClosed:  {"bead": {...}, "closed_by": "..."}
type sseBeadWrapper struct {
	Bead json.RawMessage `json:"bead"`
}

// sseBeadData mirrors the kbeads model.Bead fields used by the bridge.
type sseBeadData struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Title    string          `json:"title"`
	Status   string          `json:"status"`
	Assignee string          `json:"assignee"`
	Labels   []string        `json:"labels"`
	Priority int             `json:"priority"`
	Fields   json.RawMessage `json:"fields"`
}

// ParseBeadEvent extracts a bridge BeadEvent from a kbeads SSE event payload.
// Returns nil if the payload is malformed or missing a bead.
func ParseBeadEvent(data []byte) *BeadEvent {
	var wrapper sseBeadWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil
	}
	if len(wrapper.Bead) == 0 {
		return nil
	}

	var bead sseBeadData
	if err := json.Unmarshal(wrapper.Bead, &bead); err != nil {
		return nil
	}

	// Parse fields from json.RawMessage to map[string]string.
	fields := make(map[string]string)
	if len(bead.Fields) > 0 {
		// Try direct string map first.
		if err := json.Unmarshal(bead.Fields, &fields); err != nil {
			// Fall back to map[string]any.
			var raw map[string]any
			if err := json.Unmarshal(bead.Fields, &raw); err == nil {
				for k, v := range raw {
					fields[k] = fmt.Sprintf("%v", v)
				}
			}
		}
	}

	return &BeadEvent{
		ID:       bead.ID,
		Type:     bead.Type,
		Title:    bead.Title,
		Status:   bead.Status,
		Assignee: bead.Assignee,
		Labels:   bead.Labels,
		Fields:   fields,
		Priority: bead.Priority,
	}
}
