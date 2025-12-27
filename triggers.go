package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.zx2c4.com/irc/hbot"
)

// getHostmask returns the full nick!user@host string from a message prefix
func getHostmask(m *hbot.Message) string {
	if m.Prefix.User != "" && m.Prefix.Host != "" {
		return fmt.Sprintf("%s!%s@%s", m.Prefix.Name, m.Prefix.User, m.Prefix.Host)
	}
	return m.Prefix.Name
}

type Trigger struct {
	state     *BotState
	Condition func(*BotState, *hbot.Message) bool
	Action    func(*BotState, *hbot.Message)
}

func (t Trigger) Handle(bot *hbot.Bot, m *hbot.Message) {
	if t.Condition(t.state, m) {
		t.Action(t.state, m)
	}
}

var colonRe = regexp.MustCompile(` *:.*$`)

// checkFlood checks if a user is flooding and handles the ignore if so.
// Returns true if the request should be blocked (user is flooding).
func checkFlood(state *BotState, m *hbot.Message) bool {
	// Check if antiflood is enabled
	if state.cfg.FloodMaxRequests <= 0 || state.cfg.FloodWindowSeconds <= 0 || state.cfg.FloodIgnoreSeconds <= 0 {
		return false
	}

	hostmask := getHostmask(m)
	window := time.Duration(state.cfg.FloodWindowSeconds) * time.Second

	// Record request and check if flooding
	if state.flood.RecordRequest(hostmask, state.cfg.FloodMaxRequests, window) {
		// User is flooding - add to ignore list
		userHost := getUserHost(hostmask)
		pattern := "*!*@" + strings.Split(userHost, "@")[1] // *!*@host pattern
		ignoreDuration := time.Duration(state.cfg.FloodIgnoreSeconds) * time.Second

		state.ignores.Add(pattern, ignoreDuration, "antiflood")
		if err := state.ignores.Save(); err != nil {
			log.Printf("Error saving ignores after flood: %v", err)
		}

		// Notify user
		msg := fmt.Sprintf("You have been blocked for %s for flooding (%d requests in %ds).",
			FormatDuration(ignoreDuration), state.cfg.FloodMaxRequests, state.cfg.FloodWindowSeconds)
		state.bot.Notice(m.Prefix.Name, msg)

		log.Printf("Antiflood: blocked %s for %s", pattern, FormatDuration(ignoreDuration))

		// Notify controller
		if state.cfg.Controller != "" {
			ctrlMsg := fmt.Sprintf("Antiflood: blocked %s (%s) for %s",
				m.Prefix.Name, pattern, FormatDuration(ignoreDuration))
			state.bot.Msg(state.cfg.Controller, ctrlMsg)
		}

		return true
	}
	return false
}

var sendTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		if strings.HasPrefix(m.Param(0), "#") && strings.HasPrefix(m.Trailing(), "!"+state.cfg.Prefix) {
			if state.ignores.IsIgnored(getHostmask(m)) {
				log.Printf("Ignored file request from %s", getHostmask(m))
				return false
			}
			if checkFlood(state, m) {
				return false
			}
			return true
		}
		return false
	},
	Action: func(state *BotState, m *hbot.Message) {
		msg := m.Trailing()
		msg = colonRe.ReplaceAllString(msg, "")
		prefix := "!" + state.cfg.Prefix
		msg = strings.TrimPrefix(msg, prefix)
		msg = strings.TrimLeft(msg, " ")
		// msg is now hash | filename
		path := findFile(FILELIST_PATH, msg)
		if path == "" {
			state.bot.Notice(m.Prefix.Name, "File not found.")
			return
		}

		basepath := filepath.Base(path)
		state.mu.Lock()
		defer state.mu.Unlock()
		sendFile(state, m.Prefix.Name, path, basepath)
	},
}

var sendListTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		if strings.HasPrefix(m.Param(0), "#") && strings.HasPrefix(strings.ToLower(m.Trailing()), "@"+strings.ToLower(state.cfg.Prefix)) {
			if state.ignores.IsIgnored(getHostmask(m)) {
				log.Printf("Ignored list request from %s", getHostmask(m))
				return false
			}
			if checkFlood(state, m) {
				return false
			}
			return true
		}
		return false
	},
	Action: func(state *BotState, m *hbot.Message) {
		var fn string
		var listFn string
		if strings.ToLower(m.Trailing()) == "@"+strings.ToLower(state.cfg.Prefix) {
			fi, _ := os.Stat(USERLIST_PATH)
			date := fi.ModTime().Format(time.DateOnly)
			fn = state.cfg.Nick + "_" + date + ".zip"
			listFn = USERLIST_PATH
		} else {
			// Check for a list
			listname, cut := strings.CutPrefix(m.Trailing(), "@"+state.cfg.Prefix+"-")
			if !cut {
				return
			}
			if strings.Contains(listname, "/") || strings.Contains(listname, "\\") {
				return
			}
			fi, err := os.Stat("filelists/" + listname + ".zip")
			if err != nil {
				log.Printf("Looking for list %s: %s", listname, err)
				return
			}
			date := fi.ModTime().Format(time.DateOnly)
			listFn = "filelists/" + listname + ".zip"
			fn = state.cfg.Nick + "_" + listname + "_" + date + ".zip"
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		sendFile(state, m.Prefix.Name, listFn, fn)
	},
}

func sendFile(state *BotState, nick string, path string, wanted string) {
	if isQueueFull(state, nick) {
    state.bot.Notice(nick, "Your queue is full. You can only queue 3 files at once. Please wait for your current transfers to complete.")
    return
}
if shouldQueue(state, nick) {
		state.queue = append(state.queue, QueueEntry{nick: nick, filename: path, wanted: wanted})
		buf := fmt.Sprintf("%s queued in position %d.", wanted, len(state.queue))
		state.bot.Notice(nick, buf)
		return
	}
	port := getUnusedPort(state)
	state.ports[port] = nick
	go dccSend(state, nick, path, wanted, port)
}

var sendSlotsTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		return m.Command == "PING" && time.Now().Sub(state.slotsLastSent) > time.Second*3600
	},
	Action: func(state *BotState, m *hbot.Message) {
		fi, _ := os.Stat(USERLIST_PATH)
		date := fi.ModTime().Unix()
		uptime := int64(time.Now().Sub(state.connected).Seconds())
		buf := fmt.Sprintf("\u0001SLOTS 50 50 NOW 0 999 0 %d 0 0 %d %d ServBot 0.1\u0001",
			state.totalFiles, date, uptime)
		for c := range state.joinedChannels {
			if slices.Contains(state.cfg.AdChannels, c) {
				state.bot.Msg(c, buf)
			}
		}
		state.slotsLastSent = time.Now()
	},
}

var sendAdTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		return state.cfg.Ad != "" && state.cfg.AdTimer > 0 && m.Command == "PING" && time.Now().Sub(state.adLastSent) > time.Second*time.Duration(state.cfg.AdTimer)
	},
	Action: func(state *BotState, m *hbot.Message) {
		ad := state.cfg.Ad
		fi, _ := os.Stat(USERLIST_PATH)
		date := fi.ModTime().Format(time.DateOnly)
		ad = strings.ReplaceAll(ad, "$date", date)
		ad = strings.ReplaceAll(ad, "$totalFiles", strconv.FormatInt(state.totalFiles, 10))
		ad = strings.ReplaceAll(ad, "$prefix", state.cfg.Prefix)
		for c := range state.joinedChannels {
			if state.adChannels[c] {
				state.bot.Msg(c, ad)
			}
		}
		state.adLastSent = time.Now()
	},
}

var updateChannelsTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		return m.Command == "JOIN" || m.Command == "PART"
	},
	Action: func(state *BotState, m *hbot.Message) {
		if m.Prefix.Name != state.bot.Nick() {
			return
		}
		channel := m.Param(0)
		if m.Command == "JOIN" {
			log.Printf("Joined %s", channel)
			state.joinedChannels[channel] = true
		} else if m.Command == "PART" {
			log.Printf("Left %s", channel)
			delete(state.joinedChannels, channel)
		}
	},
}

var privmsgTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		return m.Command == "PRIVMSG" && m.Param(0) == state.bot.Nick()
	},
	Action: func(state *BotState, m *hbot.Message) {
		log.Printf("*%s* %s", m.Prefix.Name, m.Trailing())
		if state.cfg.Controller == "" || m.Prefix.Name != state.cfg.Controller {
			return
		}
		if m.Trailing() == "shutdown" {
			state.bot.Msg(m.Prefix.Name, "Shutting down.")
			time.Sleep(1 * time.Second)
			state.bot.Close()
			// --- PID FILE DELETION ADDED ---
			removePidFile()
			os.Exit(0)
		} else if m.Trailing() == "queue" {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("%d queued items.", len(state.queue)))
			for _, e := range state.queue {
				msg := fmt.Sprintf("%s: %s", e.nick, e.filename)
				state.bot.Msg(m.Prefix.Name, msg)
			}
		} else if m.Trailing() == "clear" {
			state.mu.Lock()
			defer state.mu.Unlock()
			state.queue = nil
			state.bot.Msg(m.Prefix.Name, "Queue cleared.")
		} else if m.Trailing() == "transfers" {
			msg := fmt.Sprintf("%v", state.ports)
			state.bot.Msg(m.Prefix.Name, msg)
// --- STATS COMMAND ADDED ---
		} else if m.Trailing() == "stats" {
			state.mu.Lock()
			stats := state.dccStats
			state.mu.Unlock()

			msg := fmt.Sprintf("DCC Stats (Day %d): %d file(s) sent, Total data: %s",
				stats.LastResetDay,
				stats.FileCount,
				formatBytes(stats.TotalBytes))

			state.bot.Msg(m.Prefix.Name, msg)

		} else if m.Trailing() == "flood" {
			if state.cfg.FloodMaxRequests <= 0 || state.cfg.FloodWindowSeconds <= 0 {
				state.bot.Msg(m.Prefix.Name, "Antiflood is disabled.")
			} else {
				msg := fmt.Sprintf("Antiflood: %d requests in %ds window, %ds ignore. Tracking %d users.",
					state.cfg.FloodMaxRequests, state.cfg.FloodWindowSeconds,
					state.cfg.FloodIgnoreSeconds, state.flood.ActiveUsers())
				state.bot.Msg(m.Prefix.Name, msg)
			}

		} else if m.Trailing() == "rehash" {
			if err := state.Rehash(); err != nil {
				state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Error reloading config: %v", err))
			} else {
				state.bot.Msg(m.Prefix.Name, "Configuration reloaded.")
			}

		} else if m.Trailing() == "genlist" {
			if state.generating {
				state.bot.Msg(m.Prefix.Name, "Generation already in progress.")
				return
			}
			state.generating = true
			state.bot.Msg(m.Prefix.Name, "generating...")
			makeFilelist(state.cfg)
			os.Rename(FILELIST_PATH+".tmp", FILELIST_PATH)
			var err error
			state.totalFiles, state.totalSize, state.lastModified, err = readList()
			if err != nil {
				state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Error reading filelist: %s", err))
			}
			state.generating = false
			state.bot.Msg(m.Prefix.Name, "File list generated.")
		} else if strings.HasPrefix(m.Trailing(), "ignore ") {
			handleIgnoreCommand(state, m)
		}
	},
}

func handleIgnoreCommand(state *BotState, m *hbot.Message) {
	parts := strings.Fields(m.Trailing())
	if len(parts) < 2 {
		state.bot.Msg(m.Prefix.Name, "Usage: ignore <add|del|list|clean> [pattern] [duration]")
		return
	}

	subCmd := parts[1]

	switch subCmd {
	case "add":
		if len(parts) < 3 {
			state.bot.Msg(m.Prefix.Name, "Usage: ignore add <user@host> [duration] [reason]")
			state.bot.Msg(m.Prefix.Name, "Duration: 10M (minutes), 24H (hours), 7D (days), perm (permanent)")
			state.bot.Msg(m.Prefix.Name, "Pattern: *!*@host, *!user@*, nick!*@* (wildcards supported)")
			return
		}

		pattern := parts[2]
		// Normalize pattern: if no ! or @, assume it's a host pattern
		if !strings.Contains(pattern, "!") && !strings.Contains(pattern, "@") {
			pattern = "*!*@" + pattern
		}

		var duration time.Duration
		var reason string
		if len(parts) >= 4 {
			d, err := ParseDuration(parts[3])
			if err == nil {
				duration = d
				if len(parts) >= 5 {
					reason = strings.Join(parts[4:], " ")
				}
			} else {
				// parts[3] is part of the reason
				reason = strings.Join(parts[3:], " ")
			}
		}

		state.ignores.Add(pattern, duration, reason)
		if err := state.ignores.Save(); err != nil {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Error saving ignores: %v", err))
			return
		}

		if duration > 0 {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Added ignore: %s (expires in %s)", pattern, FormatDuration(duration)))
		} else {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Added permanent ignore: %s", pattern))
		}

	case "del", "remove":
		if len(parts) < 3 {
			state.bot.Msg(m.Prefix.Name, "Usage: ignore del <pattern>")
			return
		}

		pattern := parts[2]
		if state.ignores.Remove(pattern) {
			if err := state.ignores.Save(); err != nil {
				state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Error saving ignores: %v", err))
				return
			}
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Removed ignore: %s", pattern))
		} else {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Pattern not found: %s", pattern))
		}

	case "list":
		entries := state.ignores.List()
		if len(entries) == 0 {
			state.bot.Msg(m.Prefix.Name, "No active ignores.")
			return
		}
		state.bot.Msg(m.Prefix.Name, fmt.Sprintf("%d active ignore(s):", len(entries)))
		for _, e := range entries {
			var expStr string
			if e.ExpiresAt != nil {
				remaining := time.Until(*e.ExpiresAt)
				expStr = fmt.Sprintf(" (expires in %s)", FormatDuration(remaining))
			} else {
				expStr = " (permanent)"
			}
			msg := e.Pattern + expStr
			if e.Reason != "" {
				msg += " - " + e.Reason
			}
			state.bot.Msg(m.Prefix.Name, msg)
		}

	case "clean":
		cleaned := state.ignores.CleanExpired()
		if err := state.ignores.Save(); err != nil {
			state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Error saving ignores: %v", err))
			return
		}
		state.bot.Msg(m.Prefix.Name, fmt.Sprintf("Cleaned %d expired ignore(s).", cleaned))

	default:
		state.bot.Msg(m.Prefix.Name, "Usage: ignore <add|del|list|clean>")
	}
}

var initialCommandTrigger = Trigger{
	Condition: func(state *BotState, m *hbot.Message) bool {
		return m.Command == "001"
	},
	Action: func(state *BotState, m *hbot.Message) {
		if state.cfg.InitialCommand != "" {
			log.Printf("Sending initial command")
			state.bot.Send(state.cfg.InitialCommand)
		}
	},
}

func newTrigger(state *BotState, t Trigger) Trigger {
	t.state = state
	return t
}
