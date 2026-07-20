package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// keyedRateLimiter is an in-process token-bucket limiter. The entry cap is
// intentional: an attacker must not be able to turn random source addresses
// into an unbounded memory allocation. Deployments with more than one app
// instance must also enforce a shared limit at the reverse proxy.
type keyedRateLimiter struct {
	mu         sync.Mutex
	visitors   map[string]*rateVisitor
	limit      rate.Limit
	burst      int
	ttl        time.Duration
	maxEntries int
	lastSweep  time.Time
}

type rateVisitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newKeyedRateLimiter(requestsPerMinute, burst, maxEntries int) *keyedRateLimiter {
	return &keyedRateLimiter{
		visitors:   make(map[string]*rateVisitor),
		limit:      rate.Limit(float64(requestsPerMinute) / 60),
		burst:      burst,
		ttl:        10 * time.Minute,
		maxEntries: maxEntries,
		lastSweep:  time.Now(),
	}
}

func (l *keyedRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= time.Minute {
		for k, visitor := range l.visitors {
			if now.Sub(visitor.lastSeen) > l.ttl {
				delete(l.visitors, k)
			}
		}
		l.lastSweep = now
	}

	visitor, ok := l.visitors[key]
	if !ok {
		if len(l.visitors) >= l.maxEntries {
			return false
		}
		visitor = &rateVisitor{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.visitors[key] = visitor
	}
	visitor.lastSeen = now
	return visitor.limiter.Allow()
}

func (l *keyedRateLimiter) middleware(key func(*gin.Context) string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.allow(key(c), time.Now()) {
			c.Header("Retry-After", "60")
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Rate limit exceeded"})
			return
		}
		c.Next()
	}
}

// concurrencyLimit sheds work immediately when all slots are occupied. This
// keeps slow upstream services and expensive forecast generation from
// exhausting the server even during a distributed burst.
func concurrencyLimit(max int) gin.HandlerFunc {
	semaphore := make(chan struct{}, max)
	return func(c *gin.Context) {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
			c.Next()
		default:
			c.Header("Retry-After", "1")
			c.Header("Cache-Control", "no-store")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Server is busy"})
		}
	}
}

func clientIPKey(c *gin.Context) string {
	return c.ClientIP()
}

func authenticatedUserKey(c *gin.Context) string {
	return c.GetString("uid")
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Firebase's same-origin helper must be passed through transparently;
		// Firebase supplies the response policy for its iframe/handler pages.
		if strings.HasPrefix(c.Request.URL.Path, "/__/auth/") || c.Request.URL.Path == "/__/firebase/init.json" {
			c.Next()
			return
		}
		c.Header("Content-Security-Policy", "base-uri 'self'; object-src 'none'; frame-ancestors 'self'")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Permissions-Policy", "camera=(), microphone=(), payment=()")
		c.Next()
	}
}

func getenvPositiveInt(key string, fallback int) int {
	value, err := strconv.Atoi(getenvDefault(key, ""))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
