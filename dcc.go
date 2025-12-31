package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall" // Added for Windows socket options
	"time"
)

// DCCStats tracks daily transfer statistics
type DCCStats struct {
	FileCount    int   `json:"file_count"`
	TotalBytes   int64 `json:"total_bytes"`
	LastResetDay int   `json:"last_reset_day"`
}

const statsFile = "dcc_stats.json"
const maxQueuePerUser = 3

// loadStats loads statistics from disk
func loadStats() *DCCStats {
	data, err := os.ReadFile(statsFile)
	if err != nil {
		return &DCCStats{LastResetDay: time.Now().YearDay()}
	}
	var stats DCCStats
	if err := json.Unmarshal(data, &stats); err != nil {
		log.Printf("Error unmarshaling stats: %v", err)
		return &DCCStats{LastResetDay: time.Now().YearDay()}
	}
	return &stats
}

func saveStats(stats *DCCStats) {
	data, err := json.Marshal(stats)
	if err != nil {
		log.Printf("Error marshaling stats: %v", err)
		return
	}
	if err := os.WriteFile(statsFile, data, 0644); err != nil {
		log.Printf("Error writing stats file: %v", err)
	}
}

func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

func checkAndReportStats(state *BotState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	now := time.Now()
	currentDay := now.YearDay()
	if state.dccStats.LastResetDay != currentDay {
		if state.dccStats.FileCount > 0 || state.dccStats.TotalBytes > 0 {
			msg := fmt.Sprintf("Daily statistics: sent %d file(s) (%s)",
				state.dccStats.FileCount,
				formatBytes(state.dccStats.TotalBytes))
			log.Printf("DCC %s", msg)
			state.bot.Msg(state.cfg.Controller, msg)
		}
		state.dccStats.FileCount = 0
		state.dccStats.TotalBytes = 0
		state.dccStats.LastResetDay = currentDay
		saveStats(state.dccStats)
	}
}

func recordTransfer(state *BotState, bytes int64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.dccStats.FileCount++
	state.dccStats.TotalBytes += bytes
	saveStats(state.dccStats)
}

func startStatsChecker(state *BotState) {
	go func() {
		for {
			now := time.Now()
			tomorrow := now.Add(24 * time.Hour)
			midnight := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 1, 0, 0, now.Location())
			time.Sleep(midnight.Sub(now))
			checkAndReportStats(state)
		}
	}()
}

func dccSend(state *BotState, nick string, fname string, wantedFname string, port int) {
	log.Printf("Sending %s to %s on port %d", fname, nick, port)
	defer freePort(state, port)

	// Use ListenConfig to set socket options for Windows/Linux port reuse
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				            // This now calls the version in socket_windows.go 
				                        // or socket_unix.go depending on build environment.
				                                    setReuseAddr(fd) 
							})
		},
	}

	l, err := lc.Listen(context.Background(), "tcp", ":"+strconv.Itoa(port))
	if err != nil {
		log.Printf("Error listening on %d: %v", port, err)
		return
	}
	defer l.Close()

	st, err := os.Stat(fname)
	if err != nil {
		log.Printf("Stat %s: %v", fname, err)
		return
	}

	ip := ip2int(state.ip)
	msg := fmt.Sprintf("\u0001DCC SEND \"%s\" %d %d %d\u0001", wantedFname, ip, port, st.Size())
	state.bot.Msg(nick, msg)

	l.(*net.TCPListener).SetDeadline(time.Now().Add(60 * time.Second))
	conn, err := l.Accept()
	if err != nil {
		log.Printf("Error accepting on %d: %v", port, err)
		return
	}
	defer conn.Close()

	fp, err := os.Open(fname)
	if err != nil {
		log.Printf("Error opening %s: %v", fname, err)
		return
	}
	defer fp.Close()

	ch := make(chan bool)
	go func() {
		buf := make([]byte, 4)
		for {
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			_, err := conn.Read(buf)
			if err != nil {
				ch <- true
				return
			}
			total := binary.BigEndian.Uint32(buf)
			if total == uint32(st.Size()) {
				ch <- true
				return
			}
		}
	}()

	n, err := io.Copy(conn, fp)
	if err != nil {
		log.Printf("Transfer error: %v", err)
	}
	<-ch
	log.Printf("Transfer of %d on %d to %s complete", n, port, nick)
	
	recordTransfer(state, n)
	state.bot.Msg(state.cfg.Controller, fmt.Sprintf("Successfully sent %s to %s", filepath.Base(fname), nick))
}

// getUnusedPort uses a Round Robin approach to avoid hammering the same port
func getUnusedPort(state *BotState) int {
	for i := 0; i < len(state.availablePorts); i++ {
		// Calculate next index to check
		idx := (state.lastPortIndex + i + 1) % len(state.availablePorts)
		port := state.availablePorts[idx]
		
		if state.ports[port] == "" && !state.closing[port] {
			state.lastPortIndex = idx // Update the last used index
			return port
		}
	}
	return -1
}

// freePort marks a port as closing and waits for OS cleanup before triggering next queue item
func freePort(state *BotState, port int) {
	state.mu.Lock()
	if _, ok := state.ports[port]; !ok {
		state.mu.Unlock()
		return
	}
	delete(state.ports, port)
	state.closing[port] = true
	state.mu.Unlock()

	// Launch a background timer to ensure the port is released by the OS
	go func() {
		time.Sleep(time.Second * 5)
		state.mu.Lock()
		delete(state.closing, port)
		
		// NOW that the port is truly available, check if we should start a queued item
		if len(state.queue) > 0 {
			var item QueueEntry
			item, state.queue = state.queue[0], state.queue[1:]
			
			nextPort := getUnusedPort(state)
			if nextPort != -1 {
				state.ports[nextPort] = item.nick
				go dccSend(state, item.nick, item.filename, item.wanted, nextPort)
			} else {
				// If somehow still no ports, put it back at the front
				state.queue = append([]QueueEntry{item}, state.queue...)
			}
		}
		state.mu.Unlock()
	}()
}

func countQueuedFiles(state *BotState, nick string) int {
	count := 0
	for _, entry := range state.queue {
		if entry.nick == nick {
			count++
		}
	}
	return count
}

func shouldQueue(state *BotState, nick string) bool {
	for _, v := range state.ports {
		if v == nick {
			return true
		}
	}
	if len(state.ports) == len(state.availablePorts) {
		return true
	}
	return false
}

func isQueueFull(state *BotState, nick string) bool {
	return countQueuedFiles(state, nick) >= maxQueuePerUser
}

func ip2int(s string) uint32 {
	ip := net.ParseIP(s).To4()
	return binary.BigEndian.Uint32(ip)
}