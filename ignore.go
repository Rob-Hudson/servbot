package main

import (
	"encoding/json"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const IGNORES_PATH = "ignores.json"

type IgnoreEntry struct {
	Pattern   string     `json:"pattern"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	AddedAt   time.Time  `json:"added_at"`
	Reason    string     `json:"reason,omitempty"`
}

type IgnoreList struct {
	Entries []IgnoreEntry `json:"entries"`
	mu      sync.RWMutex
}

// LoadIgnores loads the ignore list from disk
func LoadIgnores() *IgnoreList {
	list := &IgnoreList{}
	data, err := os.ReadFile(IGNORES_PATH)
	if err != nil {
		if os.IsNotExist(err) {
			return list
		}
		log.Printf("Error loading ignores: %v", err)
		return list
	}
	if err := json.Unmarshal(data, list); err != nil {
		log.Printf("Error parsing ignores: %v", err)
	}
	return list
}

// Save persists the ignore list to disk
func (l *IgnoreList) Save() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(IGNORES_PATH, data, 0644)
}

// Add adds a new ignore pattern with optional duration
func (l *IgnoreList) Add(pattern string, duration time.Duration, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := IgnoreEntry{
		Pattern: pattern,
		AddedAt: time.Now(),
		Reason:  reason,
	}
	if duration != 0 {
		exp := time.Now().Add(duration)
		entry.ExpiresAt = &exp
	}

	// Remove existing entry with same pattern
	for i, e := range l.Entries {
		if e.Pattern == pattern {
			l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
			break
		}
	}

	l.Entries = append(l.Entries, entry)
}

// Remove removes an ignore pattern, returns true if found
func (l *IgnoreList) Remove(pattern string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	for i, e := range l.Entries {
		if e.Pattern == pattern {
			l.Entries = append(l.Entries[:i], l.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// IsIgnored checks if a hostmask matches any ignore pattern
func (l *IgnoreList) IsIgnored(hostmask string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	for _, e := range l.Entries {
		// Skip expired entries
		if e.ExpiresAt != nil && now.After(*e.ExpiresAt) {
			continue
		}
		if matchPattern(e.Pattern, hostmask) {
			return true
		}
	}
	return false
}

// CleanExpired removes expired entries and saves
func (l *IgnoreList) CleanExpired() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cleaned := 0
	var active []IgnoreEntry
	for _, e := range l.Entries {
		if e.ExpiresAt != nil && now.After(*e.ExpiresAt) {
			cleaned++
			continue
		}
		active = append(active, e)
	}
	l.Entries = active
	return cleaned
}

// List returns a copy of all active entries
func (l *IgnoreList) List() []IgnoreEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	var active []IgnoreEntry
	for _, e := range l.Entries {
		if e.ExpiresAt != nil && now.After(*e.ExpiresAt) {
			continue
		}
		active = append(active, e)
	}
	return active
}

// matchPattern matches a hostmask against a pattern with wildcards
// Pattern format: nick!user@host (wildcards: * matches any chars)
// Examples: *!*@bad.host.com, *!baduser@*, *!*@192.168.*
func matchPattern(pattern, hostmask string) bool {
	// Convert glob pattern to regex
	regexPattern := "^"
	for _, c := range pattern {
		switch c {
		case '*':
			regexPattern += ".*"
		case '?':
			regexPattern += "."
		case '.', '+', '^', '$', '(', ')', '[', ']', '{', '}', '|', '\\':
			regexPattern += "\\" + string(c)
		default:
			regexPattern += string(c)
		}
	}
	regexPattern += "$"

	re, err := regexp.Compile("(?i)" + regexPattern) // Case insensitive
	if err != nil {
		return false
	}
	return re.MatchString(hostmask)
}

// ParseDuration parses duration strings like "24H", "10M", "7D", "30S"
// Returns 0 for permanent ignores
func ParseDuration(s string) (time.Duration, error) {
	if s == "" || s == "0" || strings.ToLower(s) == "perm" {
		return 0, nil
	}

	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) < 2 {
		return 0, nil
	}

	numStr := s[:len(s)-1]
	unit := s[len(s)-1]

	num, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, err
	}

	switch unit {
	case 'S':
		return time.Duration(num) * time.Second, nil
	case 'M':
		return time.Duration(num) * time.Minute, nil
	case 'H':
		return time.Duration(num) * time.Hour, nil
	case 'D':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'W':
		return time.Duration(num) * 7 * 24 * time.Hour, nil
	default:
		// Try parsing as minutes if no unit
		return time.Duration(num) * time.Minute, nil
	}
}

// FormatDuration formats remaining duration in human-readable form
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	if d >= 24*time.Hour {
		days := int(d.Hours() / 24)
		return strconv.Itoa(days) + "d"
	}
	if d >= time.Hour {
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	if d >= time.Minute {
		return strconv.Itoa(int(d.Minutes())) + "m"
	}
	return strconv.Itoa(int(d.Seconds())) + "s"
}
