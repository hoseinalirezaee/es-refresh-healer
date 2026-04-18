package ratelimit

import (
	"testing"
	"time"
)

func TestCooldown(t *testing.T) {
	cooldown := NewCooldown(10 * time.Minute)
	now := time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)

	if !cooldown.Allow("apps/demo", now) {
		t.Fatalf("expected first attempt to be allowed")
	}

	cooldown.Mark("apps/demo", now)
	if cooldown.Allow("apps/demo", now.Add(9*time.Minute)) {
		t.Fatalf("expected cooldown to block repeated attempt")
	}
	if remaining := cooldown.Remaining("apps/demo", now.Add(9*time.Minute)); remaining != time.Minute {
		t.Fatalf("expected 1m cooldown remaining, got %s", remaining)
	}

	if !cooldown.Allow("apps/demo", now.Add(10*time.Minute)) {
		t.Fatalf("expected cooldown to expire")
	}
	if remaining := cooldown.Remaining("apps/demo", now.Add(10*time.Minute)); remaining != 0 {
		t.Fatalf("expected expired cooldown remaining to be 0, got %s", remaining)
	}
}

func TestLimiterCanBeDisabled(t *testing.T) {
	limiter := New(0)
	for i := 0; i < 100; i++ {
		if !limiter.Allow() {
			t.Fatalf("disabled limiter should allow all attempts")
		}
	}
}
