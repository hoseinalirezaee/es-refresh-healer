package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Limiter struct {
	limiter *rate.Limiter
}

func New(maxPerMinute int) *Limiter {
	if maxPerMinute <= 0 {
		return &Limiter{}
	}

	perSecond := rate.Limit(float64(maxPerMinute) / 60.0)
	if perSecond < rate.Limit(1.0/60.0) {
		perSecond = rate.Limit(1.0 / 60.0)
	}

	return &Limiter{
		limiter: rate.NewLimiter(perSecond, maxPerMinute),
	}
}

func (l *Limiter) Allow() bool {
	if l == nil || l.limiter == nil {
		return true
	}
	return l.limiter.Allow()
}

type Cooldown struct {
	duration time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

func NewCooldown(duration time.Duration) *Cooldown {
	return &Cooldown{
		duration: duration,
		last:     map[string]time.Time{},
	}
}

func (c *Cooldown) Allow(key string, now time.Time) bool {
	if c == nil || c.duration <= 0 {
		return true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	last, ok := c.last[key]
	if !ok {
		return true
	}
	return now.Sub(last) >= c.duration
}

func (c *Cooldown) Mark(key string, now time.Time) {
	if c == nil || c.duration <= 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.last[key] = now
}
