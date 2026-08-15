package discord

import (
	"math/rand"
	"sync"
	"time"
)

type TokenBucket struct {
	capacity   int
	tokens     int
	refillRate time.Duration
	lastRefill time.Time
	mu         sync.Mutex
}

func NewTokenBucket(capacity int, refillRate time.Duration) *TokenBucket {
	return &TokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

func (b *TokenBucket) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.tokens > 0 {
		b.tokens--
		return true
	}
	return false
}

func (b *TokenBucket) AllowN(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.tokens >= n {
		b.tokens -= n
		return true
	}
	return false
}

func (b *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := int(elapsed / b.refillRate)
	if tokensToAdd > 0 {
		b.tokens = min(b.capacity, b.tokens+tokensToAdd)
		b.lastRefill = now
	}
}

func (b *TokenBucket) Available() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens
}

func (b *TokenBucket) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens = b.capacity
	b.lastRefill = time.Now()
}

type DiscordRatelimiter struct {
	threadBucket  *TokenBucket
	messageBucket *TokenBucket
	burstMax      int

	mu          sync.Mutex
	retryDelays map[string]time.Time
}

func NewDiscordRatelimiter(threadPer15min, msgPerSec, burstMax int) *DiscordRatelimiter {
	return &DiscordRatelimiter{
		threadBucket:  NewTokenBucket(threadPer15min, 15*time.Minute/time.Duration(threadPer15min)),
		messageBucket: NewTokenBucket(msgPerSec, time.Second/time.Duration(msgPerSec)),
		burstMax:      burstMax,
		retryDelays:   make(map[string]time.Time),
	}
}

func (r *DiscordRatelimiter) AllowThread() bool {
	return r.threadBucket.Allow()
}

func (r *DiscordRatelimiter) AllowMessage() bool {
	return r.messageBucket.Allow() || r.messageBucket.AllowN(r.burstMax)
}

func (r *DiscordRatelimiter) ThreadAvailable() int {
	return r.threadBucket.Available()
}

func (r *DiscordRatelimiter) NextRetryDelay(key string, attempts int) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	base := time.Second * 15
	maxDelay := time.Minute * 5

	delay := base * time.Duration(1<<min(attempts, 5))
	if delay > maxDelay {
		delay = maxDelay
	}

	jitter := time.Duration(rand.Int63n(int64(delay/2)) + int64(delay/2))
	return jitter
}

func (r *DiscordRatelimiter) ShouldRetry(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if delay, exists := r.retryDelays[key]; exists {
		if time.Now().Before(delay) {
			return false, time.Until(delay)
		}
		delete(r.retryDelays, key)
	}
	return true, 0
}

func (r *DiscordRatelimiter) SetRetry(key string, delay time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryDelays[key] = time.Now().Add(delay)
}

func (r *DiscordRatelimiter) ClearRetry(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.retryDelays, key)
}
