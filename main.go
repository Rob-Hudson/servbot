package main

import (
	"bufio"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/alecthomas/kong"
	"golang.zx2c4.com/irc/hbot"
)

// --- ADDED CONSTANT ---
const PID_FILE_PATH = "servbot.pid"
var CLI struct {
	Serve struct {
	} `cmd:""`
	Genlist struct {
	} `cmd:""`
}

type BotState struct {
	bot *hbot.Bot
	// Map of active ports to nicks
	ports          map[int]string
	availablePorts []int
	mu             sync.Mutex
	queue          []QueueEntry
	cfg            Config
	// List stats
	totalFiles   int64
	totalSize    int64
	lastModified time.Time
	// Slots
	slotsLastSent  time.Time
	adLastSent     time.Time
	connected      time.Time
	joinedChannels map[string]bool
	adChannels     map[string]bool
	ip             string
	generating     bool
	dccStats       *DCCStats
	ignores        *IgnoreList
	flood          *FloodTracker
}


type Config struct {
	Prefix             string
	Ports              []int
	Ip                 string
	Nick               string
	Host               string
	Channels           []string
	Ad                 string
	AdChannels         []string `toml:"ad_channels"`
	AdTimer            int      `toml:"ad_timer"`
	Listpaths          []string
	InitialCommand     string `toml:"initial_command"`
	Lists              []ConfigList
	Controller         string
	RemovedPrefixes    []string `toml:"removed_prefixes"`
	InsecureSkipVerify bool     `toml:"insecure_skip_verify"`
	TLSFingerprints    []string `toml:"tls_fingerprints"`
	// Antiflood settings
	FloodMaxRequests    int `toml:"flood_max_requests"`    // Max requests in window (0 = disabled)
	FloodWindowSeconds  int `toml:"flood_window_seconds"`  // Time window in seconds
	FloodIgnoreSeconds  int `toml:"flood_ignore_seconds"`  // How long to ignore flooder
}

type ConfigList struct {
	Name  string
	Paths []string
}

type QueueEntry struct {
	nick     string
	filename string
	wanted   string
}

// --- ADDED FUNCTION ---
func createPidFile() {
    pid := os.Getpid()
    err := ioutil.WriteFile(PID_FILE_PATH, []byte(strconv.Itoa(pid)+"\n"), 0644)
    if err != nil {
        log.Fatalf("Failed to create PID file %s: %v", PID_FILE_PATH, err)
    }
    log.Printf("Created PID file: %s (PID: %d)", PID_FILE_PATH, pid)
}

func removePidFile() {
    err := os.Remove(PID_FILE_PATH)
    if err != nil && !os.IsNotExist(err) {
        log.Printf("Warning: Failed to remove PID file %s: %v", PID_FILE_PATH, err)
    }
}


func main() {    // --- ADDED PID FILE CREATION ---
    createPidFile()
    defer removePidFile()
    

	
	if _, ok := os.LookupEnv("INVOCATION_ID"); ok {
		log.SetFlags(log.Flags() &^ (log.Ldate | log.Ltime))
	}
	state := BotState{}
	_, err := toml.DecodeFile("config.toml", &state.cfg)
	if err != nil {
		log.Fatalf("Loading config: %v", err)
	}
	state.dccStats = loadStats()
	state.ignores = LoadIgnores()
	log.Printf("Loaded %d ignore entries", len(state.ignores.Entries))
	state.flood = NewFloodTracker()
	if state.cfg.FloodMaxRequests > 0 && state.cfg.FloodWindowSeconds > 0 && state.cfg.FloodIgnoreSeconds > 0 {
		log.Printf("Antiflood enabled: %d requests in %ds window, %ds ignore",
			state.cfg.FloodMaxRequests, state.cfg.FloodWindowSeconds, state.cfg.FloodIgnoreSeconds)
	}
	startStatsChecker(&state)
	state.ports = make(map[int]string)
	state.joinedChannels = make(map[string]bool)
	state.adChannels = make(map[string]bool)
	for _, c := range state.cfg.AdChannels {
		state.adChannels[c] = true
	}
	state.availablePorts = state.cfg.Ports
	ctx := kong.Parse(&CLI)
	switch ctx.Command() {
	case "serve":
		serve(&state)
	case "genlist":
		makeFilelist(state.cfg)
		os.Rename(FILELIST_PATH+".tmp", FILELIST_PATH)
	default:
		panic(ctx.Command())
	}
}
	func serve(state *BotState) {
	var err error
	state.totalFiles, state.totalSize, state.lastModified, err = readList()
	if err != nil {
		panic(err)
	}
	log.Printf("Loaded list of %d files, total size %d bytes", state.totalFiles, state.totalSize)
	state.bot = hbot.NewBot(&hbot.Config{
		Host:     state.cfg.Host,
		Nick:     state.cfg.Nick,
		Realname: "Servbot",
		Channels: state.cfg.Channels,
		Logger:   hbot.Logger{Verbosef: log.Printf, Errorf: log.Printf},
		TLSConfig: tls.Config{
			VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
				return verifyPeerCertificate(state.cfg, rawCerts, verifiedChains)
			},
			InsecureSkipVerify: state.cfg.InsecureSkipVerify,
		},
	})
	state.bot.AddTrigger(newTrigger(state, sendTrigger))
	state.bot.AddTrigger(newTrigger(state, sendListTrigger))
	state.bot.AddTrigger(newTrigger(state, sendSlotsTrigger))
	state.bot.AddTrigger(newTrigger(state, sendAdTrigger))
	state.bot.AddTrigger(newTrigger(state, updateChannelsTrigger))
	state.bot.AddTrigger(newTrigger(state, privmsgTrigger))
	state.bot.AddTrigger(newTrigger(state, initialCommandTrigger))

	if state.cfg.Ip == "auto" {
		var err error
		state.ip, err = getIp()
		if err != nil {
			log.Fatal("Getting IP", err)
		}
		log.Println("Obtained IP address:", state.ip)
	} else {
		state.ip = state.cfg.Ip
	}

	for {
		state.connected = time.Now()
		state.bot.Run()
		time.Sleep(time.Second * 5)
	}
}

// Reads the list and returns the number of files and total size, in bytes.
// This is needed for SLOTS announcements.
func readList() (totalFiles int64, totalSize int64, lastModified time.Time, err error) {
	fp, err := os.Open(FILELIST_PATH)
	if err != nil {
		return
	}
	fi, err := fp.Stat()
	if err != nil {
		return
	}
	lastModified = fi.ModTime()
	scanner := bufio.NewScanner(fp)
	for scanner.Scan() {
		items := strings.Split(scanner.Text(), "\t")
		var size int64
		size, err = strconv.ParseInt(items[2], 10, 64)
		if err != nil {
			return
		}
		totalSize += size
		totalFiles += 1
	}
	return
}

func getIp() (string, error) {
	res, err := http.Get("https://api.ipify.org")
	if err != nil {
		return "", err
	}
	ip, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if len(ip) > 16 {
		return "", errors.New("IP address too long")
	}
	return string(ip), nil
}

func verifyPeerCertificate(config Config, rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
	if verifiedChains != nil {
		return nil // Already verified
	}
	var fingerprints []string
	for _, c := range rawCerts {
		f := fmt.Sprintf("%x", sha1.Sum(c))
		fingerprints = append(fingerprints, f)
		if slices.Contains(config.TLSFingerprints, f) {
			return nil // Verified
		}
	}
	// Nothing was verified, parse the certs
	fmt.Printf("TLS verification failed, and no fingerprints matched in the configuration file.\n")
	for i, c := range rawCerts {
		parsedCert, err := x509.ParseCertificate(c)
		if err != nil {
			return err
		}
		fmt.Printf("%d: subject=%s\n", i, parsedCert.Subject)
		fmt.Printf("Fingerprint: %s\n", fingerprints[i])
	}
	return errors.New("No fingerprint matched")
}
