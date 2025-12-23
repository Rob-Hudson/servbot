package main

import (
	"testing"
	"time"
)

func TestGetUserHost(t *testing.T) {
	tests := []struct {
		hostmask string
		want     string
	}{
		{"nick!user@host.com", "user@host.com"},
		{"nick!~user@192.168.1.1", "~user@192.168.1.1"},
		{"justhost", "justhost"},
		{"nick!user@host.example.org", "user@host.example.org"},
	}

	for _, tt := range tests {
		got := getUserHost(tt.hostmask)
		if got != tt.want {
			t.Errorf("getUserHost(%q) = %q, want %q", tt.hostmask, got, tt.want)
		}
	}
}

func TestFloodTracker_RecordRequest(t *testing.T) {
	ft := NewFloodTracker()
	hostmask := "nick!user@host.com"
	maxRequests := 3
	window := 10 * time.Second

	// First 3 requests should not trigger flooding
	for i := 0; i < maxRequests; i++ {
		if ft.RecordRequest(hostmask, maxRequests, window) {
			t.Errorf("Request %d should not trigger flood", i+1)
		}
	}

	// 4th request should trigger flooding
	if !ft.RecordRequest(hostmask, maxRequests, window) {
		t.Error("4th request should trigger flood")
	}
}

func TestFloodTracker_GetRequestCount(t *testing.T) {
	ft := NewFloodTracker()
	hostmask := "nick!user@host.com"
	window := 10 * time.Second

	// Record 5 requests
	for i := 0; i < 5; i++ {
		ft.RecordRequest(hostmask, 10, window)
	}

	count := ft.GetRequestCount(hostmask, window)
	if count != 5 {
		t.Errorf("GetRequestCount() = %d, want 5", count)
	}
}

func TestFloodTracker_ActiveUsers(t *testing.T) {
	ft := NewFloodTracker()
	window := 10 * time.Second

	if ft.ActiveUsers() != 0 {
		t.Error("New tracker should have 0 active users")
	}

	ft.RecordRequest("nick1!user1@host1.com", 10, window)
	ft.RecordRequest("nick2!user2@host2.com", 10, window)

	if ft.ActiveUsers() != 2 {
		t.Errorf("ActiveUsers() = %d, want 2", ft.ActiveUsers())
	}
}

func TestFloodTracker_Clean(t *testing.T) {
	ft := NewFloodTracker()

	// Record a request
	ft.RecordRequest("nick!user@host.com", 10, time.Hour)

	if ft.ActiveUsers() != 1 {
		t.Error("Should have 1 active user before clean")
	}

	// Clean with zero maxAge should remove all entries
	ft.Clean(0)

	if ft.ActiveUsers() != 0 {
		t.Errorf("After clean(0), should have 0 active users, got %d", ft.ActiveUsers())
	}
}

func TestFloodTracker_SameHostDifferentNicks(t *testing.T) {
	ft := NewFloodTracker()
	maxRequests := 3
	window := 10 * time.Second

	// Same user@host but different nicks should be tracked together
	ft.RecordRequest("nick1!user@host.com", maxRequests, window)
	ft.RecordRequest("nick2!user@host.com", maxRequests, window)
	ft.RecordRequest("nick3!user@host.com", maxRequests, window)

	// 4th request should trigger flood (same user@host)
	if !ft.RecordRequest("nick4!user@host.com", maxRequests, window) {
		t.Error("4th request from same user@host should trigger flood")
	}
}
