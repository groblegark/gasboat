package reconciler

import (
	"log/slog"
	"sync"
	"time"
)

// CreationRateLimiter tracks pod creation timestamps over a sliding window
// and trips when too many pods are created too quickly. Once tripped, it
// blocks further creations until the window expires.
type CreationRateLimiter struct {
	mu        sync.Mutex
	timestamps []time.Time
	limit     int
	window    time.Duration
	logger    *slog.Logger
	nowFunc   func() time.Time // for testing
}

// NewCreationRateLimiter creates a rate limiter. If limit <= 0, the limiter
// is disabled (Allow always returns true).
func NewCreationRateLimiter(limit int, window time.Duration, logger *slog.Logger) *CreationRateLimiter {
	return &CreationRateLimiter{
		limit:   limit,
		window:  window,
		logger:  logger,
		nowFunc: time.Now,
	}
}

// Allow returns true if a new pod creation is permitted under the rate limit.
// It does NOT record the creation — call Record after a successful create.
func (rl *CreationRateLimiter) Allow() bool {
	if rl.limit <= 0 {
		return true // disabled
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.pruneExpired()
	return len(rl.timestamps) < rl.limit
}

// Record records a successful pod creation timestamp.
func (rl *CreationRateLimiter) Record() {
	if rl.limit <= 0 {
		return
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.timestamps = append(rl.timestamps, rl.nowFunc())
}

// Count returns the number of creations within the current window.
func (rl *CreationRateLimiter) Count() int {
	if rl.limit <= 0 {
		return 0
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.pruneExpired()
	return len(rl.timestamps)
}

// pruneExpired removes timestamps outside the sliding window.
// Must be called with rl.mu held.
func (rl *CreationRateLimiter) pruneExpired() {
	cutoff := rl.nowFunc().Add(-rl.window)
	i := 0
	for i < len(rl.timestamps) && rl.timestamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		rl.timestamps = rl.timestamps[i:]
	}
}
