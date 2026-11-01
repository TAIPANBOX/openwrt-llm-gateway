package main

import (
	"sync"
	"time"
)

// limiter holds each client to its rate (a token bucket refilled evenly over a minute) and to
// its count of requests in flight. A model server in a house serves one request at a time well;
// one device must not be able to queue everyone else out.
type limiter struct {
	mu    sync.Mutex
	state map[string]*bucket
}

type bucket struct {
	tokens   float64
	last     time.Time
	inFlight int
}

func newLimiter() *limiter { return &limiter{state: map[string]*bucket{}} }

// take admits one request for c at now, or says why not. The caller runs release when the
// request is done.
func (l *limiter) take(c *Client, now time.Time) (release func(), why string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.state[c.Name]
	if b == nil {
		b = &bucket{tokens: float64(c.Rate), last: now}
		l.state[c.Name] = b
	}
	if c.Concurrent > 0 && b.inFlight >= c.Concurrent {
		return nil, "concurrency"
	}
	if c.Rate > 0 {
		b.tokens += now.Sub(b.last).Minutes() * float64(c.Rate)
		if b.tokens > float64(c.Rate) {
			b.tokens = float64(c.Rate)
		}
		b.last = now
		if b.tokens < 1 {
			return nil, "rate"
		}
		b.tokens--
	}
	b.inFlight++
	return func() {
		l.mu.Lock()
		b.inFlight--
		l.mu.Unlock()
	}, ""
}
