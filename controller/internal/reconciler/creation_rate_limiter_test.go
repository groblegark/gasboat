package reconciler

import (
	"testing"
	"time"
)

func TestCreationRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := NewCreationRateLimiter(3, 5*time.Minute, testLogger())

	for i := 0; i < 3; i++ {
		if !rl.Allow() {
			t.Fatalf("expected Allow()=true at count %d", i)
		}
		rl.Record()
	}

	if rl.Allow() {
		t.Error("expected Allow()=false after reaching limit")
	}
}

func TestCreationRateLimiter_ExpiresOldEntries(t *testing.T) {
	rl := NewCreationRateLimiter(2, 5*time.Minute, testLogger())

	now := time.Now()
	rl.nowFunc = func() time.Time { return now }

	// Fill to limit.
	rl.Record()
	rl.Record()
	if rl.Allow() {
		t.Fatal("expected Allow()=false at limit")
	}

	// Advance time past the window.
	rl.nowFunc = func() time.Time { return now.Add(6 * time.Minute) }

	if !rl.Allow() {
		t.Error("expected Allow()=true after window expiration")
	}
	if rl.Count() != 0 {
		t.Errorf("Count() = %d, want 0 after expiration", rl.Count())
	}
}

func TestCreationRateLimiter_DisabledWhenZero(t *testing.T) {
	rl := NewCreationRateLimiter(0, 5*time.Minute, testLogger())

	for i := 0; i < 100; i++ {
		if !rl.Allow() {
			t.Fatalf("expected disabled limiter to always Allow(), failed at %d", i)
		}
		rl.Record()
	}

	if rl.Count() != 0 {
		t.Errorf("disabled limiter Count() = %d, want 0", rl.Count())
	}
}

func TestCreationRateLimiter_SlidingWindow(t *testing.T) {
	rl := NewCreationRateLimiter(3, 10*time.Minute, testLogger())

	now := time.Now()
	rl.nowFunc = func() time.Time { return now }

	// Record 2 at t=0.
	rl.Record()
	rl.Record()

	// Advance 6 minutes, record 1 more.
	now = now.Add(6 * time.Minute)
	rl.Record()

	// At t=6m, all 3 are within the 10m window.
	if rl.Allow() {
		t.Error("expected Allow()=false with 3 in window")
	}

	// Advance to t=11m — the first 2 (from t=0) expire.
	now = now.Add(5 * time.Minute)
	if !rl.Allow() {
		t.Error("expected Allow()=true after oldest entries expire")
	}
	if rl.Count() != 1 {
		t.Errorf("Count() = %d, want 1 (only t=6m entry remains)", rl.Count())
	}
}
