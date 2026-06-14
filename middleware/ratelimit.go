// Package middleware provides per-shop rate limiting using a token-bucket algorithm.
// For single-node Contabo deployment this in-memory implementation is sufficient.
// Replace with Redis for multi-node.
package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type bucket struct {
	tokens   float64
	lastFill time.Time
	mu       sync.Mutex
}

type RateLimiter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
	// maxTokens is the burst limit; refillRate is tokens added per second.
	maxTokens  float64
	refillRate float64
}

// NewRateLimiter creates a limiter where each shop can burst up to maxTokens
// requests and refills at refillRate per second.
// Example: NewRateLimiter(30, 1) → 1 req/s sustained, 30 burst.
func NewRateLimiter(maxTokens, refillRate float64) *RateLimiter {
	rl := &RateLimiter{
		buckets:    make(map[string]*bucket),
		maxTokens:  maxTokens,
		refillRate: refillRate,
	}
	// Background eviction of stale buckets (every 10 min).
	go func() {
		for range time.Tick(10 * time.Minute) {
			rl.evict()
		}
	}()
	return rl
}

func (rl *RateLimiter) Allow(shop string) bool {
	b := rl.getBucket(shop)
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens = min(rl.maxTokens, b.tokens+elapsed*rl.refillRate)
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (rl *RateLimiter) getBucket(shop string) *bucket {
	rl.mu.RLock()
	if b, ok := rl.buckets[shop]; ok {
		rl.mu.RUnlock()
		return b
	}
	rl.mu.RUnlock()

	rl.mu.Lock()
	defer rl.mu.Unlock()
	if b, ok := rl.buckets[shop]; ok {
		return b
	}
	b := &bucket{tokens: rl.maxTokens, lastFill: time.Now()}
	rl.buckets[shop] = b
	return b
}

func (rl *RateLimiter) evict() {
	cutoff := time.Now().Add(-30 * time.Minute)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, b := range rl.buckets {
		b.mu.Lock()
		old := b.lastFill.Before(cutoff)
		b.mu.Unlock()
		if old {
			delete(rl.buckets, k)
		}
	}
}

// Limit returns a Gin middleware that enforces the rate limit per shop.
func (rl *RateLimiter) Limit() gin.HandlerFunc {
	return func(c *gin.Context) {
		shop := ShopFrom(c)
		if shop == "" {
			c.Next()
			return
		}
		if !rl.Allow(shop) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded — please slow down",
			})
			return
		}
		c.Next()
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
