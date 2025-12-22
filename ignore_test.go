package main

import (
	"testing"
	"time"
)

func TestMatchPattern(t *testing.T) {
	tests := []struct {
		pattern  string
		hostmask string
		want     bool
	}{
		// Exact match
		{"nick!user@host.com", "nick!user@host.com", true},
		{"nick!user@host.com", "nick!user@other.com", false},

		// Wildcard in nick
		{"*!user@host.com", "anynick!user@host.com", true},
		{"*!user@host.com", "anynick!other@host.com", false},

		// Wildcard in user
		{"nick!*@host.com", "nick!anyuser@host.com", true},
		{"nick!*@host.com", "other!anyuser@host.com", false},

		// Wildcard in host
		{"nick!user@*", "nick!user@anyhost.com", true},
		{"nick!user@*", "nick!other@anyhost.com", false},

		// Full wildcard except host (common ban pattern)
		{"*!*@bad.host.com", "anyone!anyuser@bad.host.com", true},
		{"*!*@bad.host.com", "anyone!anyuser@good.host.com", false},

		// Partial host wildcard (subnet-style)
		{"*!*@192.168.1.*", "user!ident@192.168.1.100", true},
		{"*!*@192.168.1.*", "user!ident@192.168.2.100", false},
		{"*!*@*.evil.net", "spammer!spam@sub.evil.net", true},
		{"*!*@*.evil.net", "spammer!spam@evil.net", false},

		// Wildcard user match
		{"*!baduser@*", "anyone!baduser@anyhost.com", true},
		{"*!baduser@*", "anyone!gooduser@anyhost.com", false},

		// Case insensitivity
		{"*!*@BAD.HOST.COM", "user!ident@bad.host.com", true},
		{"*!*@bad.host.com", "USER!IDENT@BAD.HOST.COM", true},

		// Question mark single char wildcard
		{"*!*@192.168.1.?", "user!ident@192.168.1.5", true},
		{"*!*@192.168.1.?", "user!ident@192.168.1.55", false},

		// Complex patterns
		{"*!*@*.subdomain.*.com", "nick!user@prefix.subdomain.middle.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_vs_"+tt.hostmask, func(t *testing.T) {
			got := matchPattern(tt.pattern, tt.hostmask)
			if got != tt.want {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.pattern, tt.hostmask, got, tt.want)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"10S", 10 * time.Second},
		{"10s", 10 * time.Second},
		{"5M", 5 * time.Minute},
		{"5m", 5 * time.Minute},
		{"24H", 24 * time.Hour},
		{"24h", 24 * time.Hour},
		{"7D", 7 * 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"2W", 2 * 7 * 24 * time.Hour},
		{"2w", 2 * 7 * 24 * time.Hour},
		{"perm", 0},
		{"PERM", 0},
		{"0", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseDuration(tt.input)
			if err != nil {
				t.Errorf("ParseDuration(%q) error = %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIgnoreList(t *testing.T) {
	list := &IgnoreList{}

	// Test adding and checking
	list.Add("*!*@bad.host.com", 0, "test reason")
	if !list.IsIgnored("anyone!user@bad.host.com") {
		t.Error("Should be ignored: anyone!user@bad.host.com")
	}
	if list.IsIgnored("anyone!user@good.host.com") {
		t.Error("Should NOT be ignored: anyone!user@good.host.com")
	}

	// Test timed ignore
	list.Add("*!*@temp.host.com", 1*time.Hour, "temporary")
	if !list.IsIgnored("user!ident@temp.host.com") {
		t.Error("Should be ignored: user!ident@temp.host.com")
	}

	// Test expired ignore
	list.Add("*!*@expired.host.com", -1*time.Hour, "already expired")
	if list.IsIgnored("user!ident@expired.host.com") {
		t.Error("Should NOT be ignored (expired): user!ident@expired.host.com")
	}

	// Test removal
	list.Remove("*!*@bad.host.com")
	if list.IsIgnored("anyone!user@bad.host.com") {
		t.Error("Should NOT be ignored after removal")
	}

	// Test List() excludes expired
	entries := list.List()
	for _, e := range entries {
		if e.Pattern == "*!*@expired.host.com" {
			t.Error("List() should not include expired entries")
		}
	}

	// Test CleanExpired
	cleaned := list.CleanExpired()
	if cleaned != 1 {
		t.Errorf("CleanExpired() = %d, want 1", cleaned)
	}
}
