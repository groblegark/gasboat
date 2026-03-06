package reconciler

import (
	"log/slog"
	"sync"
	"time"
)

// SpawnRateLimiter tracks pod creation timestamps in a sliding window and
// blocks further creations when the count exceeds a threshold. This prevents
// runaway pod stampedes (e.g., 70+ pods in minutes after a redeploy).
type SpawnRateLimiter struct {
	mu        sync.Mutex
	maxCount  int
	window    time.Duration
	stamps    []time.Time
	limited   bool
	logger    *slog.Logger
	nowFunc   func() time.Time // for testing
}

// NewSpawnRateLimiter creates a rate limiter that allows at most maxCount pod
// creations within the given window. If maxCount <= 0, the limiter is disabled
// (Allow always returns true).
func NewSpawnRateLimiter(maxCount int, window time.Duration, logger *slog.Logger) *SpawnRateLimiter {
	return &SpawnRateLimiter{
		maxCount: maxCount,
		window:   window,
		logger:   logger,
		nowFunc:  time.Now,
	}
}

// Allow checks whether a pod creation is permitted. Returns false when the
// sliding window already contains maxCount creations.
func (r *SpawnRateLimiter) Allow() bool {
	if r.maxCount <= 0 {
		return true // disabled
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc()
	r.pruneExpired(now)

	if len(r.stamps) >= r.maxCount {
		if !r.limited {
			r.limited = true
			r.logger.Warn("spawn rate limit reached",
				"count", len(r.stamps),
				"window", r.window,
				"limit", r.maxCount)
		}
		return false
	}
	return true
}

// Record marks a successful pod creation in the sliding window.
// Call this after a pod has been created, not before.
func (r *SpawnRateLimiter) Record() {
	if r.maxCount <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.stamps = append(r.stamps, r.nowFunc())
}

// IsLimited returns true if the rate limiter is currently blocking creations.
func (r *SpawnRateLimiter) IsLimited() bool {
	if r.maxCount <= 0 {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.pruneExpired(r.nowFunc())

	wasLimited := r.limited
	if len(r.stamps) < r.maxCount && r.limited {
		r.limited = false
		r.logger.Info("spawn rate limit cleared",
			"count", len(r.stamps),
			"limit", r.maxCount)
	}
	_ = wasLimited
	return r.limited
}

// pruneExpired removes timestamps older than the window. Must be called with mu held.
func (r *SpawnRateLimiter) pruneExpired(now time.Time) {
	cutoff := now.Add(-r.window)
	i := 0
	for i < len(r.stamps) && r.stamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		r.stamps = r.stamps[i:]
	}
}
