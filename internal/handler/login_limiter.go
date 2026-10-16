package handler

import (
	"sync"
	"time"
)

// ponytail: in-memory per-IP fixed window; resets on restart, IP rotation evades it. Move to a persistent or proxy-level limiter if attacks appear.
type loginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	now     func() time.Time
	entries map[string]loginLimitEntry
}

// loginLimitEntry is one IP's failure count and the window it started at.
type loginLimitEntry struct {
	start time.Time
	count int
}

func newLoginLimiter(max int, window time.Duration, now func() time.Time) *loginLimiter {
	return &loginLimiter{
		max:     max,
		window:  window,
		now:     now,
		entries: make(map[string]loginLimitEntry),
	}
}

// blocked reports whether ip has recorded at least max failures inside a
// window that has not yet ended.
func (l *loginLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[ip]
	if !ok {
		return false
	}
	if !l.now().Before(e.start.Add(l.window)) {
		return false
	}
	return e.count >= l.max
}

// fail records one failed login for ip: the first failure starts its window,
// and entries whose window has ended are pruned.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for k, e := range l.entries {
		if !now.Before(e.start.Add(l.window)) {
			delete(l.entries, k)
		}
	}

	e, ok := l.entries[ip]
	if !ok {
		l.entries[ip] = loginLimitEntry{start: now, count: 1}
		return
	}
	e.count++
	l.entries[ip] = e
}

// reset clears ip's failure history after a successful login.
func (l *loginLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.entries, ip)
}
