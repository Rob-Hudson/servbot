package main

import "testing"

func TestFindFile(t *testing.T) {
	wanted := []struct {
		str  string
		want string
	}{
		{"h1 | file1", "/1/file1"},
		{"file1", "/1/file1"},
		{"h2 | file1", "/2/file1"},
	}
	for _, s := range wanted {
		match := findFile("test/list", s.str)
		if match != s.want {
			t.Fatalf("matching %q, expected %q, got %q", s.str, s.want, match)
		}
	}
}

func TestHumanSize(t *testing.T) {
	wanted := []struct {
		size int64
		want string
	}{
		{500, "500B"},
		{1024, "1.00KB"},
		{1536, "1.50KB"},
		{1523962, "1.45MB"},
		{1500000000, "1.40GB"},
	}
	for _, s := range wanted {
		match := humanSize(s.size)
		if match != s.want {
			t.Fatalf("want %s got %s input %d", s.want, match, s.size)
		}
	}
}
