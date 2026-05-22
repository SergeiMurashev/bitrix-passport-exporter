package httpapi

import (
	"sync"
	"time"
)

type rateWindowLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	store  map[string]rateWindowCounter
}

type rateWindowCounter struct {
	count       int
	windowStart time.Time
}

func newRateWindowLimiter(limit int, window time.Duration) *rateWindowLimiter {
	if limit <= 0 || window <= 0 {
		return nil
	}
	return &rateWindowLimiter{
		limit:  limit,
		window: window,
		store:  make(map[string]rateWindowCounter),
	}
}

func (l *rateWindowLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	counter, ok := l.store[key]
	if !ok || now.Sub(counter.windowStart) >= l.window {
		l.store[key] = rateWindowCounter{
			count:       1,
			windowStart: now,
		}
		return true
	}

	if counter.count >= l.limit {
		return false
	}
	counter.count++
	l.store[key] = counter
	return true
}
