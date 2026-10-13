package pathbay

import (
	"sync"
	"time"
)

type missEntry struct {
	window time.Time
	until  time.Time
	count  int
}

type missLimiter struct {
	mu      sync.Mutex
	entries map[string]missEntry
	cleanAt time.Time
}

func (l *missLimiter) retryAfter(ip string, at time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[ip]
	if at.Before(e.until) {
		return int((e.until.Sub(at) + time.Second - 1) / time.Second)
	}
	if !e.until.IsZero() || (!e.window.IsZero() && at.Sub(e.window) >= time.Minute) {
		delete(l.entries, ip)
	}
	return 0
}

func (l *missLimiter) record(ip string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]missEntry)
	}
	if !at.Before(l.cleanAt) {
		for key, e := range l.entries {
			if !at.Before(e.until) && at.Sub(e.window) >= time.Minute {
				delete(l.entries, key)
			}
		}
		l.cleanAt = at.Add(time.Minute)
	}
	e, exists := l.entries[ip]
	if at.Before(e.until) {
		return
	}
	if !exists && len(l.entries) >= 8192 {
		return
	}
	if e.window.IsZero() || at.Sub(e.window) >= time.Minute {
		e = missEntry{window: at}
	}
	e.count++
	if e.count >= 20 {
		e.until = at.Add(5 * time.Minute)
	}
	l.entries[ip] = e
}
