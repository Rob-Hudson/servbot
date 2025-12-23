package main

import (
	"strings"
	"sync"
	"time"
)

// FloodTracker tracks request timestamps per user for antiflood protection
type FloodTracker struct {
	// Map of "user@host" -> list of request timestamps
	requests map[string][]time.Time
	mu       sync.Mutex
}

// NewFloodTracker creates a new FloodTracker
func NewFloodTracker() *FloodTracker {
	return &FloodTracker{
		requests: make(map[string][]time.Time),
	}
}

// getUserHost extracts user@host from a full hostmask (nick!user@host)
func getUserHost(hostmask string) string {
	// Find the ! separator
	if idx := strings.Index(hostmask, "!"); idx != -1 {
		return hostmask[idx+1:]
	}
	return hostmask
}

// RecordRequest records a request from a user and returns true if they are flooding
// maxRequests: maximum requests allowed in the window
// window: time window to check
func (f *FloodTracker) RecordRequest(hostmask string, maxRequests int, window time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	userHost := getUserHost(hostmask)
	now := time.Now()
	cutoff := now.Add(-window)

	// Get existing timestamps and filter to only recent ones
	timestamps := f.requests[userHost]
	var recent []time.Time
	for _, t := range timestamps {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	// Add current request
	recent = append(recent, now)
	f.requests[userHost] = recent

	// Check if flooding (> maxRequests in window means flooding)
	return len(recent) > maxRequests
}

// GetRequestCount returns the number of recent requests from a user
func (f *FloodTracker) GetRequestCount(hostmask string, window time.Duration) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	userHost := getUserHost(hostmask)
	cutoff := time.Now().Add(-window)

	count := 0
	for _, t := range f.requests[userHost] {
		if t.After(cutoff) {
			count++
		}
	}
	return count
}

// Clean removes old entries to prevent memory leaks
func (f *FloodTracker) Clean(maxAge time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for userHost, timestamps := range f.requests {
		var recent []time.Time
		for _, t := range timestamps {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		if len(recent) == 0 {
			delete(f.requests, userHost)
		} else {
			f.requests[userHost] = recent
		}
	}
}

// ActiveUsers returns the number of users currently being tracked
func (f *FloodTracker) ActiveUsers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}
