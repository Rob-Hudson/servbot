package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// DCCStats tracks daily transfer statistics
type DCCStats struct {
	FileCount    int   `json:"file_count"`
	TotalBytes   int64 `json:"total_bytes"`
	LastResetDay int   `json:"last_reset_day"` // Day of year when stats were last reset
}

const statsFile = "dcc_stats.json"
const maxQueuePerUser = 3

// loadStats loads statistics from disk
func loadStats() *DCCStats {
	data, err := os.ReadFile(statsFile)
	if err != nil {
		// File doesn't exist or can't be read, return new stats
		return &DCCStats{LastResetDay: time.Now().YearDay()}
	}
	
	var stats DCCStats
	if err := json.Unmarshal(data, &stats); err != nil {
		log.Printf("Error unmarshaling stats: %v", err)
		return &DCCStats{LastResetDay: time.Now().YearDay()}
	}
	
	return &stats
}

// saveStats saves statistics to disk
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

// formatBytes converts bytes to human-readable format
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

// checkAndReportStats checks if it's time to report daily stats and does so if needed
func checkAndReportStats(state *BotState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	
	now := time.Now()
	currentDay := now.YearDay()
	
	// Check if we've crossed into a new day
	if state.dccStats.LastResetDay != currentDay {
		// Report previous day's statistics
		if state.dccStats.FileCount > 0 || state.dccStats.TotalBytes > 0 {
			msg := fmt.Sprintf("Daily statistics: sent %d file(s) (%s)",
				state.dccStats.FileCount,
				formatBytes(state.dccStats.TotalBytes))
			
			log.Printf("DCC %s", msg)
			state.bot.Msg(state.cfg.Controller, msg)
		}
		
		// Reset statistics for the new day
		state.dccStats.FileCount = 0
		state.dccStats.TotalBytes = 0
		state.dccStats.LastResetDay = currentDay
		saveStats(state.dccStats)
	}
}

// recordTransfer records a successful file transfer
func recordTransfer(state *BotState, bytes int64) {
	state.mu.Lock()
	defer state.mu.Unlock()
	
	state.dccStats.FileCount++
	state.dccStats.TotalBytes += bytes
	saveStats(state.dccStats)
}

// startStatsChecker starts a goroutine that checks for day rollover at 12:01 AM
func startStatsChecker(state *BotState) {
	go func() {
		for {
			now := time.Now()
			// Calculate time until 12:01 AM tomorrow
			tomorrow := now.Add(24 * time.Hour)
			midnight := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 1, 0, 0, now.Location())
			duration := midnight.Sub(now)
			
			time.Sleep(duration)
			checkAndReportStats(state)
		}
	}()
}

func dccSend(state *BotState, nick string, fname string, wantedFname string, port int) {
	log.Printf("Sending %s to %s on port %d", fname, nick, port)
	defer freePort(state, port)
	addr, err := net.ResolveTCPAddr("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		log.Printf("Error resolving: %v", err)
		return
	}
	l, err := net.ListenTCP("tcp", addr)
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
	// The receiver might not accept the file, and might not connect. Guard against that case.
	l.SetDeadline(time.Now().Add(60 * time.Second))
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
	conn.Close()
	log.Printf("Transfer of %d on %d to %s complete", n, port, nick)
	
	// Record successful transfer
	recordTransfer(state, n)
	
	 state.bot.Msg(state.cfg.Controller, fmt.Sprintf("Successfully sent %s to %s", filepath.Base(fname), nick))
}

func getUnusedPort(state *BotState) int {
	for _, port := range state.availablePorts {
		if state.ports[port] == "" && !state.closing[port] {
			return port
		}
	}
	return -1
}

// Frees the transfer on this port, and starts the next queued item if any.
func freePort(state *BotState, port int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if _, ok := state.ports[port]; !ok {
		return
	}
	delete(state.ports, port)
	state.closing[port] = true
	go func() {
		time.Sleep(time.Second * 5)
		state.mu.Lock()
		defer state.mu.Unlock()
		delete(state.closing, port)
	}()
	if len(state.queue) > 0 {
		var item QueueEntry
		item, state.queue = state.queue[0], state.queue[1:]
		port := getUnusedPort(state)
		if port != -1 {
			state.ports[port] = item.nick
			go dccSend(state, item.nick, item.filename, item.wanted, port)
		} else {
			state.queue = append([]QueueEntry{item}, state.queue...)
		}
	}
}

// countQueuedFiles counts how many files a user has in the queue
func countQueuedFiles(state *BotState, nick string) int {
	count := 0
	for _, entry := range state.queue {
		if entry.nick == nick {
			count++
		}
	}
	return count
}

// Checks to see whether an entry should be queued.
// Returns true if the nick is already being sent to, or if all ports are used.
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

// isQueueFull checks if a user has reached their queue limit
// Returns true if the user has maxQueuePerUser or more files queued
func isQueueFull(state *BotState, nick string) bool {
	return countQueuedFiles(state, nick) >= maxQueuePerUser
}

func ip2int(s string) uint32 {
	ip := net.ParseIP(s).To4()
	return binary.BigEndian.Uint32(ip)
}
