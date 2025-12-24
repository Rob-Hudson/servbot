// Package hbot is IRCv3 enabled framework for writing IRC bots
package hbot

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bot implements an irc bot to be connected to a given server
type Bot struct {
	config     Config
	con        net.Conn
	outgoing   chan string
	handlers   []Handler
	mu         sync.Mutex
	joinOnce   sync.Once
	closeOnce  sync.Once
	wg         sync.WaitGroup
	capHandler ircCaps
	prefix     Prefix
	prefixMu   sync.RWMutex
	joined     chan struct{}
}

type Logger struct {
	Verbosef func(format string, args ...interface{})
	Errorf   func(format string, args ...interface{})
}

func DiscardLogf(format string, args ...interface{}) {}

type TLSKnob int

const (
	AutoTLS TLSKnob = iota
	YesTLS
	NoTLS
)

type Config struct {
	Host            string
	Nick            string
	Realname        string
	User            string
	Password        string
	Channels        []string
	SASL            bool
	MsgSafetyBuffer bool                                         // Set it if long messages get truncated on the receiving end
	Dial            func(network, addr string) (net.Conn, error) // An optional custom function for connecting to the server
	ThrottleDelay   time.Duration                                // Duration to wait between sending of messages to avoid being kicked by the server for flooding (default 200ms)
	PingTimeout     time.Duration                                // Maximum time between incoming data
	UseTLS          TLSKnob
	TLSConfig       tls.Config
	Logger          Logger
}

// CommonBotUserPrefix may be optionally used as a user prefix or realname to identify as a bot.
// e.g. `/mode #CHANNEL +q $~x:*!~botsan-*@*` or `/mode #CHANNEL +q $~r:botsan-*`.
const CommonBotUserPrefix = "botsan-"

// NewBot creates a new instance of Bot
func NewBot(conf *Config) *Bot {
	bot := Bot{config: *conf, joined: make(chan struct{})}

	if bot.config.ThrottleDelay == 0 {
		bot.config.ThrottleDelay = 200 * time.Millisecond
	}
	if bot.config.PingTimeout == 0 {
		bot.config.PingTimeout = 300 * time.Second
	}
	if bot.config.Logger.Verbosef == nil {
		bot.config.Logger.Verbosef = DiscardLogf
	}
	if bot.config.Logger.Errorf == nil {
		bot.config.Logger.Errorf = DiscardLogf
	}
	if len(bot.config.Realname) == 0 {
		bot.config.Realname = bot.config.Nick
	}
	if len(bot.config.User) == 0 {
		bot.config.User = bot.config.Nick
	}
	if len(bot.config.Nick) > 16 {
		bot.config.Nick = bot.config.Nick[:16]
	}

	bot.AddTrigger(pingPong)
	bot.AddTrigger(joinChannels)
	bot.AddTrigger(getPrefix)
	bot.AddTrigger(setNick)
	bot.AddTrigger(nickError)
	bot.AddTrigger(&bot.capHandler)
	bot.AddTrigger(saslFail)
	bot.AddTrigger(saslSuccess)
	bot.AddTrigger(errorLogger)
	return &bot
}

// Joined returns a channel that closes after channels are joined
func (bot *Bot) Joined() <-chan struct{} {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	return bot.joined
}

// saslAuthenticate performs SASL authentication
// ref: https://github.com/atheme/charybdis/blob/master/doc/sasl.txt
func (bot *Bot) saslAuthenticate(user, pass string) {
	bot.capHandler.saslEnable()
	bot.capHandler.saslCreds(user, pass)
	bot.config.Logger.Verbosef("Beginning sasl authentication")
	bot.Send("CAP LS")
	bot.sendUserCommand(bot.config.User, bot.config.Realname, "0")
	bot.SetNick(bot.config.Nick)
}

// standardRegistration performs a basic set of registration commands
func (bot *Bot) standardRegistration() {
	bot.Send("CAP LS")
	// Server registration
	if bot.config.Password != "" {
		bot.Send("PASS " + bot.config.Password)
	}
	bot.config.Logger.Verbosef("Sending standard registration")
	bot.sendUserCommand(bot.config.User, bot.config.Realname, "0")
	bot.SetNick(bot.config.Nick)
}

// Set username, real name, and mode
func (bot *Bot) sendUserCommand(user, realname, mode string) {
	bot.Send(fmt.Sprintf("USER %s %s * :%s", user, mode, realname))
}

func (bot *Bot) connect(host string) error {
	bot.config.Logger.Verbosef("Connecting")

	dial := bot.config.Dial
	if dial == nil {
		dial = net.Dial
	}
	var err error
	bot.con, err = dial("tcp", host)
	if err != nil {
		return err
	}
	if bot.config.UseTLS == AutoTLS {
		_, portStr, _ := net.SplitHostPort(host)
		port, _ := strconv.Atoi(portStr)
		switch port {
		case 6667:
			bot.config.UseTLS = NoTLS
		case 6697:
			bot.config.UseTLS = YesTLS
		default:
			bot.config.Logger.Errorf("Warning: port is neither the standard TCP port (6667) nor the standard TLS port (6697); falling back to TCP")
			bot.config.UseTLS = NoTLS
		}
	}
	if bot.config.UseTLS == YesTLS {
		if len(bot.config.TLSConfig.ServerName) == 0 {
			hostname, _, err := net.SplitHostPort(host)
			if err != nil {
				return err
			}
			bot.config.TLSConfig.ServerName = hostname
		}
		bot.con = tls.Client(bot.con, &bot.config.TLSConfig)
	}
	return nil
}

// Incoming message gathering routine
func (bot *Bot) handleIncomingMessages() {
	defer bot.wg.Done()
	scan := bufio.NewScanner(bot.con)
	for scan.Scan() {
		// Disconnect if we have seen absolutely nothing for 300 seconds
		bot.con.SetDeadline(time.Now().Add(bot.config.PingTimeout))
		text := scan.Text()
		msg := ParseMessage(text)
		go func() {
			for _, h := range bot.handlers {
				go h.Handle(bot, msg)
			}
		}()
	}
	bot.close("incoming", scan.Err())
}

// Handles message speed throtling
func (bot *Bot) handleOutgoingMessages() {
	defer bot.wg.Done()
	for s := range bot.outgoing {
		_, err := fmt.Fprint(bot.con, s+"\r\n")
		if err != nil {
			bot.close("outgoing", err)
			return
		}
		time.Sleep(bot.config.ThrottleDelay)
	}
}

// Run starts the bot and connects to the server. Blocks until we disconnect from the server.
func (bot *Bot) Run() {
	bot.mu.Lock()
	bot.joinOnce = sync.Once{}
	bot.closeOnce = sync.Once{}
	bot.wg = sync.WaitGroup{}
	bot.capHandler.reset()
	bot.outgoing = make(chan string, 128)
	bot.prefix = Prefix{
		Name: bot.config.Nick,
		User: bot.config.Nick,
		Host: strings.Repeat("*", 510-353-len(bot.config.Nick)*2),
	}
	bot.mu.Unlock()

	// Attempt reconnection
	err := bot.connect(bot.config.Host)
	if err != nil {
		bot.config.Logger.Errorf("Connection error: %v", err)
		return
	}
	bot.config.Logger.Verbosef("Connected")

	bot.wg.Add(2)
	go bot.handleIncomingMessages()
	go bot.handleOutgoingMessages()
	if bot.config.SASL {
		bot.saslAuthenticate(bot.config.Nick, bot.config.Password)
	} else {
		bot.standardRegistration()
	}
	bot.wg.Wait()
	bot.mu.Lock()
	bot.joined = make(chan struct{})
	bot.mu.Unlock()
	bot.config.Logger.Verbosef("Disconnected")
}

// CapStatus returns whether the server capability is enabled and present
func (bot *Bot) CapStatus(cap string) (enabled, present bool) {
	bot.capHandler.mu.Lock()
	defer bot.capHandler.mu.Unlock()
	if v, ok := bot.capHandler.capsEnabled[cap]; ok {
		return v, true
	}
	return false, false
}

func (bot *Bot) close(fault string, err error) {
	bot.closeOnce.Do(func() {
		if err != nil {
			bot.config.Logger.Errorf("Closing from %s: %v", fault, err)
		}
		bot.con.Close()
		select {
		case bot.outgoing <- "PING":
		default:
		}
	})
}

// Close closes the bot
func (bot *Bot) Close() {
	bot.close("", nil)
}

// Prefix returns the bot's prefix.
func (bot *Bot) Prefix() Prefix {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	return bot.prefix
}

// Nick returns the bot's nick.
func (bot *Bot) Nick() string {
	bot.mu.Lock()
	defer bot.mu.Unlock()
	return bot.config.Nick
}

// Handler is used to subscribe and react to events on the bot Server
type Handler interface {
	Handle(*Bot, *Message)
}

// Trigger is a Handler which is guarded by a condition.
// DO NOT alter *Message in your triggers or you'll have strange things happen.
type Trigger struct {
	// Returns true if this trigger applies to the passed in message
	Condition func(*Bot, *Message) bool

	// The action to perform if Condition is true
	Action func(*Bot, *Message)
}

// AddTrigger adds a trigger to the bot's handlers
func (bot *Bot) AddTrigger(h Handler) {
	bot.handlers = append(bot.handlers, h)
}

// Handle executes the trigger action if the condition is satisfied
func (t Trigger) Handle(bot *Bot, m *Message) {
	if t.Condition(bot, m) {
		t.Action(bot, m)
	}
}
