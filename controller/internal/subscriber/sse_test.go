package subscriber

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestSSEWatcher_ParsesAgentCreatedEvent verifies that an SSE "beads.bead.created"
// event for an agent bead is correctly translated to an AgentSpawn lifecycle event.
func TestSSEWatcher_ParsesAgentCreatedEvent(t *testing.T) {
	// Start an SSE server that sends one agent created event then closes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events/stream" {
			http.NotFound(w, r)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send an agent bead created event.
		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-abc123",
				"title":  "test agent",
				"type":   "agent",
				"status": "in_progress",
				"labels": []string{"gt:agent"},
				"fields": map[string]string{
					"project": "kbeads",
					"role":    "devops",
					"agent":   "worker-1",
					"mode":    "crew",
				},
				"assignee":   "human",
				"created_by": "human",
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:1\n")
		fmt.Fprintf(w, "event:beads.bead.created\n")
		fmt.Fprintf(w, "data:%s\n\n", data)
		flusher.Flush()

		// Give the watcher time to read, then close.
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Topics:        "beads.bead.*",
		Namespace:     "test-ns",
		CoopImage:     "test-image:latest",
		BeadsGRPCAddr: "localhost:9090",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentSpawn {
			t.Fatalf("expected AgentSpawn, got %s", event.Type)
		}
		if event.Project != "kbeads" {
			t.Fatalf("expected project kbeads, got %s", event.Project)
		}
		if event.Role != "devops" {
			t.Fatalf("expected role devops, got %s", event.Role)
		}
		if event.AgentName != "worker-1" {
			t.Fatalf("expected agent worker-1, got %s", event.AgentName)
		}
		if event.BeadID != "kd-abc123" {
			t.Fatalf("expected bead ID kd-abc123, got %s", event.BeadID)
		}
		if event.Metadata["namespace"] != "test-ns" {
			t.Fatalf("expected namespace test-ns, got %s", event.Metadata["namespace"])
		}
		if event.Metadata["image"] != "test-image:latest" {
			t.Fatalf("expected image test-image:latest, got %s", event.Metadata["image"])
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}

	cancel()

	// Verify last event ID was tracked.
	if w.LastEventID() != "1" {
		t.Fatalf("expected last event ID 1, got %s", w.LastEventID())
	}
}

// TestSSEWatcher_ParsesAgentClosedEvent verifies that a closed event maps to AgentDone.
func TestSSEWatcher_ParsesAgentClosedEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-close1",
				"type":   "agent",
				"status": "closed",
				"fields": map[string]string{"project": "p", "role": "qa", "agent": "a1"},
			},
			"closed_by": "human",
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:42\nevent:beads.bead.closed\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentDone {
			t.Fatalf("expected AgentDone, got %s", event.Type)
		}
		if event.BeadID != "kd-close1" {
			t.Fatalf("expected bead ID kd-close1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_SkipsNonAgentBeads verifies that non-agent beads are ignored.
func TestSSEWatcher_SkipsNonAgentBeads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send a task bead (not agent).
		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-task1",
				"type":   "task",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:1\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()

		// Then send an agent bead.
		payload2 := map[string]any{
			"bead": map[string]any{
				"id":     "kd-agent1",
				"type":   "agent",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data2, _ := json.Marshal(payload2)
		fmt.Fprintf(w, "id:2\nevent:beads.bead.created\ndata:%s\n\n", data2)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		// The first event should be the agent bead, not the task bead.
		if event.BeadID != "kd-agent1" {
			t.Fatalf("expected first event to be kd-agent1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_LastEventIDSentOnReconnect verifies that the Last-Event-ID
// header is sent when reconnecting.
func TestSSEWatcher_LastEventIDSentOnReconnect(t *testing.T) {
	requestCount := 0
	lastEventIDSeen := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		lastEventIDSeen = r.Header.Get("Last-Event-ID")

		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		if requestCount == 1 {
			// First connection: send an event then close.
			payload := map[string]any{
				"bead": map[string]any{
					"id":     "kd-r1",
					"type":   "agent",
					"status": "in_progress",
					"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
				},
			}
			data, _ := json.Marshal(payload)
			fmt.Fprintf(w, "id:99\nevent:beads.bead.created\ndata:%s\n\n", data)
			flusher.Flush()
			time.Sleep(50 * time.Millisecond)
			return // close connection to trigger reconnect
		}

		// Second connection: verify Last-Event-ID and send another event.
		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-r2",
				"type":   "agent",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a2"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:100\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Read first event.
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-r1" {
			t.Fatalf("expected kd-r1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for first event")
	}

	// Read second event (after reconnect).
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-r2" {
			t.Fatalf("expected kd-r2, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for second event")
	}

	cancel()

	// Verify the Last-Event-ID was sent on reconnection.
	if lastEventIDSeen != "99" {
		t.Fatalf("expected Last-Event-ID 99 on reconnect, got %q", lastEventIDSeen)
	}
}

// TestSSEWatcher_AgentStopOnUpdated verifies that updated events with
// agent_state=stopping map to AgentStop.
func TestSSEWatcher_AgentStopOnUpdated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":          "kd-stop1",
				"type":        "agent",
				"status":      "in_progress",
				"agent_state": "stopping",
				"fields":      map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:5\nevent:beads.bead.updated\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.Type != AgentStop {
			t.Fatalf("expected AgentStop, got %s", event.Type)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_KeepaliveIgnored verifies that keepalive comments don't break parsing.
func TestSSEWatcher_KeepaliveIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send keepalive comments before the actual event.
		fmt.Fprintf(w, ":keepalive\n\n")
		flusher.Flush()
		fmt.Fprintf(w, ":keepalive\n\n")
		flusher.Flush()

		payload := map[string]any{
			"bead": map[string]any{
				"id":     "kd-ka1",
				"type":   "agent",
				"status": "in_progress",
				"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
			},
		}
		data, _ := json.Marshal(payload)
		fmt.Fprintf(w, "id:3\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	select {
	case event := <-w.Events():
		if event.BeadID != "kd-ka1" {
			t.Fatalf("expected kd-ka1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event")
	}
	cancel()
}

// TestSSEWatcher_TopicFilterInURL verifies that topic filter is passed as query param.
func TestSSEWatcher_TopicFilterInURL(t *testing.T) {
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Close immediately.
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Topics:        "beads.bead.*,beads.label.*",
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Wait for at least one connection attempt.
	time.Sleep(500 * time.Millisecond)
	cancel()

	expected := "/v1/events/stream?topics=beads.bead.*,beads.label.*"
	if receivedPath != expected {
		t.Fatalf("expected URL %q, got %q", expected, receivedPath)
	}
}

// makeAgentPayload builds a JSON SSE payload for an agent bead with the given ID.
func makeAgentPayload(beadID string) []byte {
	payload := map[string]any{
		"bead": map[string]any{
			"id":     beadID,
			"type":   "agent",
			"status": "in_progress",
			"fields": map[string]string{"project": "p", "role": "devops", "agent": "a1"},
		},
	}
	data, _ := json.Marshal(payload)
	return data
}

// TestSSEWatcher_ReconnectsOnServerClose verifies that the watcher reconnects
// after the server closes the connection, and continues receiving events from
// the new connection.
func TestSSEWatcher_ReconnectsOnServerClose(t *testing.T) {
	var connCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connCount.Add(1)
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Each connection sends one event with a unique bead ID, then closes.
		beadID := fmt.Sprintf("kd-conn%d", n)
		data := makeAgentPayload(beadID)
		fmt.Fprintf(w, "id:%d\nevent:beads.bead.created\ndata:%s\n\n", n, data)
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		// Return to close the connection and trigger reconnect.
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Read event from first connection.
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-conn1" {
			t.Fatalf("expected kd-conn1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for first event")
	}

	// Read event from second connection (after automatic reconnect).
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-conn2" {
			t.Fatalf("expected kd-conn2, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for second event (reconnect)")
	}

	cancel()

	// Verify that at least two connections were made.
	if connCount.Load() < 2 {
		t.Fatalf("expected at least 2 connections, got %d", connCount.Load())
	}
}

// TestSSEWatcher_ServerReturns500 verifies that the watcher retries with backoff
// when the server returns HTTP 500. The second request should arrive after a delay.
func TestSSEWatcher_ServerReturns500(t *testing.T) {
	var requestTimes []time.Time
	var connCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := connCount.Add(1)
		requestTimes = append(requestTimes, time.Now())

		if n == 1 {
			// First request: return 500.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Second request: return valid SSE with an event.
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		data := makeAgentPayload("kd-after500")
		fmt.Fprintf(w, "id:1\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// Should eventually receive the event from the second (successful) connection.
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-after500" {
			t.Fatalf("expected kd-after500, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event after 500 retry")
	}

	cancel()

	// Verify backoff: second request should arrive at least 900ms after first
	// (initial backoff is 1s).
	if len(requestTimes) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(requestTimes))
	}
	delay := requestTimes[1].Sub(requestTimes[0])
	if delay < 900*time.Millisecond {
		t.Fatalf("expected backoff of at least 900ms, got %s", delay)
	}
}

// TestSSEWatcher_MalformedSSEData verifies that corrupt JSON in the data: field
// does not crash the watcher and that subsequent good events are still delivered.
func TestSSEWatcher_MalformedSSEData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send a malformed event (corrupt JSON).
		fmt.Fprintf(w, "id:1\nevent:beads.bead.created\ndata:{corrupt json!!! not valid\n\n")
		flusher.Flush()

		// Send a valid event after the bad one.
		data := makeAgentPayload("kd-good1")
		fmt.Fprintf(w, "id:2\nevent:beads.bead.created\ndata:%s\n\n", data)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// The malformed event should be skipped; we should get the good event.
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-good1" {
			t.Fatalf("expected kd-good1, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out: watcher may have crashed on malformed JSON")
	}

	// Verify the last event ID advanced past the bad event.
	if w.LastEventID() != "2" {
		t.Fatalf("expected last event ID 2, got %s", w.LastEventID())
	}

	cancel()
}

// TestSSEWatcher_ContextCancellation verifies that canceling the context during
// a reconnection backoff causes the watcher to stop promptly without hanging.
func TestSSEWatcher_ContextCancellation(t *testing.T) {
	// Server always returns 500 to force the watcher into repeated backoff.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Let the watcher make at least one failed attempt and enter backoff.
	time.Sleep(500 * time.Millisecond)

	// Cancel during backoff.
	cancel()

	// Watcher should stop within a short time (well before a full 1s backoff cycle).
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-nil error from canceled watcher")
		}
		// The error should wrap context.Canceled.
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop promptly after context cancellation")
	}
}

// TestSSEWatcher_MissingEventField verifies that an SSE message with data: but
// no event: line is handled gracefully (skipped, no crash) and doesn't block
// subsequent valid events.
func TestSSEWatcher_MissingEventField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		// Send an SSE message with data but no event: line.
		data := makeAgentPayload("kd-noevent")
		fmt.Fprintf(w, "id:1\ndata:%s\n\n", data)
		flusher.Flush()

		// Send a proper event after.
		data2 := makeAgentPayload("kd-proper")
		fmt.Fprintf(w, "id:2\nevent:beads.bead.created\ndata:%s\n\n", data2)
		flusher.Flush()
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	w := NewSSEWatcher(SSEConfig{
		BeadsHTTPAddr: srv.URL,
		Namespace:     "ns",
	}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() { _ = w.Start(ctx) }()

	// The missing-event message should be skipped; we should get the proper one.
	select {
	case event := <-w.Events():
		if event.BeadID != "kd-proper" {
			t.Fatalf("expected kd-proper, got %s", event.BeadID)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for event after missing-event-field message")
	}

	// Verify lastEventID still advanced through the no-event message.
	if w.LastEventID() != "2" {
		t.Fatalf("expected last event ID 2, got %s", w.LastEventID())
	}

	cancel()
}
