package main

import "testing"

func TestIp2int(t *testing.T) {
	res := ip2int("127.0.0.1")
	if res != 2130706433 {
		t.Fatalf("want %d got %d", 2130706433, res)
	}
}
