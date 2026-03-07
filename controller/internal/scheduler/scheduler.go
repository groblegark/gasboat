// Package scheduler watches schedule config beads and spawns agents on cron.
//
// The Scheduler is integrated into the controller's periodic sync loop.
// Each sync, it loads schedule beads from the daemon, diffs them against
// its in-memory cron entries, and adds/removes/updates entries as needed.
// When a cron fires, it calls daemon.SpawnAgent to create an agent bead,
// which the reconciler picks up and schedules a K8s pod for.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"gasboat/controller/internal/beadsapi"
)

// Spawner creates a new agent bead and returns its ID.
type Spawner interface {
	SpawnAgent(ctx context.Context, agentName, project, taskID, role, customPrompt string) (string, error)
	UpdateBeadFields(ctx context.Context, beadID string, fields map[string]string) error
}

// Lister fetches schedule beads from the daemon.
type Lister interface {
	ListScheduleBeads(ctx context.Context) ([]beadsapi.ScheduleBead, error)
}

// entry tracks a registered cron entry and its source bead.
type entry struct {
	beadID  string
	cronID  cron.EntryID
	cronExp string
	project string
	role    string
	prompt  string
	title   string
}

// Scheduler manages cron-based agent spawning driven by schedule beads.
type Scheduler struct {
	cron    *cron.Cron
	spawner Spawner
	lister  Lister
	logger  *slog.Logger

	mu      sync.Mutex
	entries map[string]*entry // beadID -> entry
}

// New creates a Scheduler. Call Sync periodically to reconcile schedule beads.
func New(spawner Spawner, lister Lister, logger *slog.Logger) *Scheduler {
	c := cron.New(cron.WithLocation(time.UTC))
	return &Scheduler{
		cron:    c,
		spawner: spawner,
		lister:  lister,
		logger:  logger,
		entries: make(map[string]*entry),
	}
}

// Start begins the cron scheduler. Must be called once before Sync.
func (s *Scheduler) Start() {
	s.cron.Start()
	s.logger.Info("scheduler started")
}

// Stop gracefully shuts down the cron scheduler.
func (s *Scheduler) Stop() context.Context {
	s.logger.Info("scheduler stopping")
	return s.cron.Stop()
}

// Sync reconciles the in-memory cron entries with schedule beads from the
// daemon. It adds new entries, removes deleted ones, and updates changed ones.
func (s *Scheduler) Sync(ctx context.Context) error {
	beads, err := s.lister.ListScheduleBeads(ctx)
	if err != nil {
		return fmt.Errorf("loading schedule beads: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Build a set of desired bead IDs.
	desired := make(map[string]beadsapi.ScheduleBead, len(beads))
	for _, b := range beads {
		if b.Enabled {
			desired[b.ID] = b
		}
	}

	// Remove entries for beads that no longer exist or are disabled.
	for id, e := range s.entries {
		if _, ok := desired[id]; !ok {
			s.cron.Remove(e.cronID)
			delete(s.entries, id)
			s.logger.Info("removed schedule entry", "bead", id, "title", e.title)
		}
	}

	// Add or update entries.
	for id, b := range desired {
		existing, exists := s.entries[id]
		if exists && existing.cronExp == b.Cron && existing.prompt == b.Prompt && existing.project == b.Project && existing.role == b.Role {
			continue // no change
		}

		// Remove old entry if updating.
		if exists {
			s.cron.Remove(existing.cronID)
			delete(s.entries, id)
		}

		// Resolve timezone.
		loc := time.UTC
		if b.Timezone != "" {
			parsed, err := time.LoadLocation(b.Timezone)
			if err != nil {
				s.logger.Warn("invalid timezone in schedule bead, using UTC",
					"bead", id, "timezone", b.Timezone, "error", err)
			} else {
				loc = parsed
			}
		}

		// Build cron spec with timezone prefix.
		spec := b.Cron
		if loc != time.UTC {
			spec = fmt.Sprintf("CRON_TZ=%s %s", loc.String(), b.Cron)
		}

		// Capture for closure.
		beadID := id
		bead := b

		cronID, err := s.cron.AddFunc(spec, func() {
			s.fire(beadID, bead)
		})
		if err != nil {
			s.logger.Warn("invalid cron expression in schedule bead",
				"bead", id, "cron", b.Cron, "error", err)
			continue
		}

		s.entries[id] = &entry{
			beadID:  id,
			cronID:  cronID,
			cronExp: b.Cron,
			project: b.Project,
			role:    b.Role,
			prompt:  b.Prompt,
			title:   b.Title,
		}

		action := "added"
		if exists {
			action = "updated"
		}
		s.logger.Info(action+" schedule entry",
			"bead", id, "title", b.Title, "cron", b.Cron, "project", b.Project)
	}

	return nil
}

// fire is called by the cron scheduler when a schedule fires.
func (s *Scheduler) fire(beadID string, bead beadsapi.ScheduleBead) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.logger.Info("schedule firing",
		"bead", beadID, "title", bead.Title, "project", bead.Project)

	// Generate agent name from schedule title.
	agentName := generateAgentName(bead.Title)

	agentBeadID, err := s.spawner.SpawnAgent(ctx, agentName, bead.Project, "", bead.Role, bead.Prompt)
	if err != nil {
		s.logger.Error("scheduled spawn failed",
			"bead", beadID, "title", bead.Title, "error", err)
		return
	}

	s.logger.Info("scheduled spawn succeeded",
		"schedule_bead", beadID, "agent_bead", agentBeadID, "agent", agentName)

	// Update last_run and last_agent_id on the schedule bead (best-effort).
	now := time.Now().UTC().Format(time.RFC3339)
	if err := s.spawner.UpdateBeadFields(ctx, beadID, map[string]string{
		"last_run":      now,
		"last_agent_id": agentBeadID,
	}); err != nil {
		s.logger.Warn("failed to update schedule bead last_run",
			"bead", beadID, "error", err)
	}
}

// EntryCount returns the number of active schedule entries (for metrics/logging).
func (s *Scheduler) EntryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// generateAgentName creates a slug from a schedule title with a timestamp suffix.
func generateAgentName(title string) string {
	// Slugify: lowercase, keep alphanumeric, replace spaces with hyphens, max 3 words.
	words := splitWords(title)
	var slug []string
	for _, w := range words {
		var clean []byte
		for _, c := range []byte(w) {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
				clean = append(clean, c)
			}
		}
		if len(clean) > 0 {
			slug = append(slug, string(clean))
		}
		if len(slug) == 3 {
			break
		}
	}
	if len(slug) == 0 {
		slug = []string{"scheduled"}
	}

	// Use date as suffix for daily uniqueness.
	suffix := time.Now().UTC().Format("0102")
	result := ""
	for i, s := range slug {
		if i > 0 {
			result += "-"
		}
		result += s
	}
	return result + "-" + suffix
}

// splitWords splits a string into lowercase words.
func splitWords(s string) []string {
	var words []string
	var current []byte
	for _, c := range []byte(s) {
		if c == ' ' || c == '-' || c == '_' || c == '/' || c == ':' {
			if len(current) > 0 {
				words = append(words, string(current))
				current = nil
			}
		} else {
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		words = append(words, string(current))
	}
	return words
}
