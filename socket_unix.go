//go:build !windows

package main

import (
	"syscall"
)

func setReuseAddr(fd uintptr) error {
	// On Unix, fd is already an int, so no Handle cast is needed
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}