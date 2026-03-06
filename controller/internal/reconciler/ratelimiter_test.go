package reconciler

import (
	"testing"
	"time"
)

func TestSpawnRateLimiter_AllowsUpToLimit(t *testing.T) {
	rl := NewSpawnRateLimiter(3, 5*time.Minute, testLogger())

	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("expected Allow()=true for creation %d", i)
		}
		rl.Record()
	}

	if rl.Allow() {
		t.Error("expected Allow()=false after reaching limit")
	}
}

func TestSpawnRateLimiter_SlidingWindowExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewSpawnRateLimiter(2, 5*time.Minute, testLogger())
	rl.nowFunc = func() time.Time { return now }

	// Fill the window.
	rl.Record()
	rl.Record()
	if rl.Allow() {
		t.Fatal("expected rate limited after 2 records")
	}

	// Advance time past the window.
	now = now.Add(6 * time.Minute)
	if !rl.Allow() {
		t.Error("expected Allow()=true after window expired")
	}
}

func TestSpawnRateLimiter_PartialExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewSpawnRateLimiter(2, 5*time.Minute, testLogger())
	rl.nowFunc = func() time.Time { return now }

	// Record first at t=0.
	rl.Record()

	// Record second at t=3m.
	now = now.Add(3 * time.Minute)
	rl.Record()

	// At t=3m, both are within the window. Should be rate limited.
	if rl.Allow() {
		t.Fatal("expected rate limited with 2 records in window")
	}

	// At t=5m01s, the first record expires. One slot opens up.
	now = now.Add(2*time.Minute + 1*time.Second)
	if !rl.Allow() {
		t.Error("expected Allow()=true after oldest record expired")
	}
}

func TestSpawnRateLimiter_DisabledWhenZeroLimit(t *testing.T) {
	rl := NewSpawnRateLimiter(0, 5*time.Minute, testLogger())

	for i := 0; i < 100; i++ {
		if !rl.Allow() {
			t.Fatal("expected disabled limiter to always allow")
		}
		rl.Record()
	}
	if rl.IsLimited() {
		t.Error("expected IsLimited()=false when disabled")
	}
}

func TestSpawnRateLimiter_IsLimited(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rl := NewSpawnRateLimiter(1, 5*time.Minute, testLogger())
	rl.nowFunc = func() time.Time { return now }

	if rl.IsLimited() {
		t.Error("expected not limited initially")
	}

	rl.Record()
	// Trigger the limited flag via Allow().
	rl.Allow()

	if !rl.IsLimited() {
		t.Error("expected limited after exceeding count")
	}

	// Expire the record.
	now = now.Add(6 * time.Minute)
	if rl.IsLimited() {
		t.Error("expected not limited after window expiry")
	}
}
