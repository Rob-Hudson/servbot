package hbot

import (
	"fmt"
	"strings"
	"time"
)

// A trigger to respond to the servers ping pong messages.
// If PingPong messages are not responded to, the server assumes the
// client has timed out and will close the connection.
var pingPong = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "PING"
	},
	Action: func(bot *Bot, m *Message) {
		bot.Send("PONG :" + m.Trailing())
	},
}

var joinChannels = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "001" || m.Command == "372"
	},
	Action: func(bot *Bot, m *Message) {
		// Ugly hack: sleep for 3 seconds to let nickserv identify us
		time.Sleep(3 * time.Second)
		bot.joinOnce.Do(func() {
			for _, channel := range bot.config.Channels {
				splitchan := strings.SplitN(channel, ":", 2)
				bot.config.Logger.Verbosef("Joining %q", channel)
				if len(splitchan) == 2 {
					channel = splitchan[0]
					password := splitchan[1]
					bot.Send(fmt.Sprintf("JOIN %s %s", channel, password))
				} else {
					bot.Send(fmt.Sprintf("JOIN %s", channel))
				}
			}
			// Fire Joined
			select {
			case <-bot.joined:
			default:
				close(bot.joined)
			}
		})
	},
}

// Get bot's prefix by catching its own join
var getPrefix = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "JOIN" && m.Prefix.Name == bot.Nick()
	},
	Action: func(bot *Bot, m *Message) {
		bot.mu.Lock()
		bot.config.Nick = m.Prefix.Name
		bot.prefix = m.Prefix
		bot.mu.Unlock()
	},
}

// Track nick changes internally so we can adjust the bot's prefix
var setNick = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "NICK" && m.Prefix.Name == bot.Nick()
	},
	Action: func(bot *Bot, m *Message) {
		to := m.Param(0)
		bot.mu.Lock()
		bot.config.Nick = to
		bot.prefix.Name = to
		bot.mu.Unlock()
		bot.config.Logger.Verbosef("Nick changed to %q", to)
	},
}

// Throw errors on invalid nick changes
var nickError = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "436" || m.Command == "433" ||
			m.Command == "432" || m.Command == "431" || m.Command == "400"
	},
	Action: func(bot *Bot, m *Message) {
		bot.config.Logger.Errorf("Nick change error %q: %q", m.Command, m.Params)
	},
}

var saslFail = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "904" || m.Command == "905" ||
			m.Command == "906" || m.Command == "907"
	},
	Action: func(bot *Bot, m *Message) {
		bot.config.Logger.Errorf("SASL error %q: %q", m.Command, m.Params)
	},
}

var saslSuccess = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "900" || m.Command == "903"
	},
	Action: func(bot *Bot, m *Message) {
		bot.config.Logger.Verbosef("SASL success: %q", m.Trailing())
	},
}

var errorLogger = Trigger{
	Condition: func(bot *Bot, m *Message) bool {
		return m.Command == "ERROR"
	},
	Action: func(bot *Bot, m *Message) {
		bot.config.Logger.Errorf("Server error: %q", m.Trailing())
	},
}
