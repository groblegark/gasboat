// Package bridge provides the JIRA sync-back watcher.
//
// JiraSync subscribes to kbeads SSE bead updated/closed events, filters for
// beads with the source:jira label, and syncs status changes, MR links, and
// closing comments back to the originating JIRA issue.
package bridge

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// syncTTL is the dedup window for sync-back operations.
const syncTTL = 10 * time.Minute

// JiraSync watches bead SSE events and syncs MR links and status back to JIRA.
type JiraSync struct {
	jira               *JiraClient
	logger             *slog.Logger
	disableTransitions bool

	mu   sync.Mutex
	seen map[string]time.Time // dedup key → last sync time
}

// JiraSyncConfig holds configuration for the JiraSync watcher.
type JiraSyncConfig struct {
	Jira               *JiraClient
	Logger             *slog.Logger
	DisableTransitions bool
}

// NewJiraSync creates a new JIRA sync-back watcher.
func NewJiraSync(cfg JiraSyncConfig) *JiraSync {
	return &JiraSync{
		jira:               cfg.Jira,
		logger:             cfg.Logger,
		disableTransitions: cfg.DisableTransitions,
		seen:               make(map[string]time.Time),
	}
}

// RegisterHandlers registers SSE event handlers on the given stream for
// bead updated and closed events.
func (s *JiraSync) RegisterHandlers(stream *SSEStream) {
	stream.On("beads.bead.updated", s.handleUpdated)
	stream.On("beads.bead.closed", s.handleClosed)
	s.logger.Info("JIRA sync watcher registered SSE handlers",
		"topics", []string{"beads.bead.updated", "beads.bead.closed"})
}

func (s *JiraSync) handleUpdated(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	// Only sync beads that originated from JIRA.
	jiraKey := jiraKeyFromBead(*bead)
	if jiraKey == "" {
		return
	}

	// Check for MR URL field — sync it as a remote link to JIRA.
	mrURL := bead.Fields["mr_url"]
	if mrURL == "" {
		return
	}

	// Dedup: don't re-sync the same MR link.
	dedupKey := "mr:" + bead.ID + ":" + mrURL
	if s.isDuplicate(dedupKey) {
		return
	}

	s.logger.Info("syncing MR link to JIRA",
		"bead", bead.ID, "jira_key", jiraKey, "mr_url", mrURL)

	// Add remote link.
	title := "Merge Request: " + bead.Title
	if err := s.jira.AddRemoteLink(ctx, jiraKey, mrURL, title); err != nil {
		s.logger.Error("failed to add JIRA remote link",
			"jira_key", jiraKey, "mr_url", mrURL, "error", err)
		return
	}

	// Add comment.
	comment := "Automated MR created: " + mrURL
	if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
		s.logger.Error("failed to add JIRA comment for MR",
			"jira_key", jiraKey, "error", err)
	}
}

func (s *JiraSync) handleClosed(ctx context.Context, data []byte) {
	bead := ParseBeadEvent(data)
	if bead == nil {
		return
	}

	jiraKey := jiraKeyFromBead(*bead)
	if jiraKey == "" {
		return
	}

	// Dedup: don't re-close the same bead.
	dedupKey := "close:" + bead.ID
	if s.isDuplicate(dedupKey) {
		return
	}

	s.logger.Info("syncing bead closure to JIRA",
		"bead", bead.ID, "jira_key", jiraKey)

	// Post closing comment.
	comment := "Task bead closed in beads system (bead " + bead.ID + ")."
	if mrURL := bead.Fields["mr_url"]; mrURL != "" {
		comment += " MR: " + mrURL
	}
	if err := s.jira.AddComment(ctx, jiraKey, comment); err != nil {
		s.logger.Error("failed to add JIRA closing comment",
			"jira_key", jiraKey, "error", err)
	}

	// Attempt to transition to "Review" (best-effort).
	if !s.disableTransitions {
		if err := s.jira.TransitionIssue(ctx, jiraKey, "Review"); err != nil {
			s.logger.Warn("failed to transition JIRA issue to Review (may not be available)",
				"jira_key", jiraKey, "error", err)
		}
	}
}

// jiraKeyFromBead extracts the JIRA key from a bead's labels or fields.
func jiraKeyFromBead(bead BeadEvent) string {
	// Check fields first (set by poller).
	if key := bead.Fields["jira_key"]; key != "" {
		return key
	}
	// Fallback: check labels for jira:<key> pattern.
	for _, label := range bead.Labels {
		if strings.HasPrefix(label, "jira:") && !strings.HasPrefix(label, "jira-label:") {
			return strings.TrimPrefix(label, "jira:")
		}
	}
	return ""
}

// isDuplicate returns true if the key was seen within the syncTTL window.
// If not, records the key and returns false.
func (s *JiraSync) isDuplicate(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Periodic cleanup.
	s.cleanupLocked()

	if t, ok := s.seen[key]; ok && time.Since(t) < syncTTL {
		return true
	}
	s.seen[key] = time.Now()
	return false
}

// cleanupLocked removes stale entries from the dedup map.
// Caller must hold s.mu.
func (s *JiraSync) cleanupLocked() {
	now := time.Now()
	for key, t := range s.seen {
		if now.Sub(t) > syncTTL {
			delete(s.seen, key)
		}
	}
}
