package gocent

import (
	"encoding/json"
	"sync"
)

// Pipe allows to send several commands in one HTTP request.
type Pipe struct {
	mu       sync.RWMutex
	commands []Command
}

// Reset allows to clear client command buffer.
func (p *Pipe) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = nil
}

func (p *Pipe) add(cmd Command) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, cmd)
	return nil
}

func (p *Pipe) addMany(commands []Command) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.commands = append(p.commands, commands...)
	return nil
}

type PublishRequest struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
	PublishOptions
}

// AddPublish adds publish command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddPublish(channel string, data []byte, opts ...PublishOption) error {
	options := &PublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "publish",
		Params: PublishRequest{
			Channel:        channel,
			Data:           data,
			PublishOptions: *options,
		},
	}
	return p.add(cmd)
}

// AddPublishRequests adds publish commands to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddPublishRequests(requests []PublishRequest) error {
	var commands []Command
	for _, request := range requests {
		commands = append(commands, Command{
			Method: "publish",
			Params: request,
		})
	}
	return p.addMany(commands)
}

type broadcastRequest struct {
	Channels []string        `json:"channels"`
	Data     json.RawMessage `json:"data"`
	PublishOptions
}

// AddBroadcast adds broadcast command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddBroadcast(channels []string, data []byte, opts ...PublishOption) error {
	options := &PublishOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "broadcast",
		Params: broadcastRequest{
			Channels:       channels,
			Data:           data,
			PublishOptions: *options,
		},
	}
	return p.add(cmd)
}

type subscribeRequest struct {
	Channel string `json:"channel"`
	User    string `json:"user"`
	SubscribeOptions
}

// AddSubscribe adds subscribe command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddSubscribe(channel string, user string, opts ...SubscribeOption) error {
	options := &SubscribeOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "subscribe",
		Params: subscribeRequest{
			Channel:          channel,
			User:             user,
			SubscribeOptions: *options,
		},
	}
	return p.add(cmd)
}

type unsubscribeRequest struct {
	Channel string `json:"channel"`
	User    string `json:"user"`
	UnsubscribeOptions
}

// AddUnsubscribe adds unsubscribe command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddUnsubscribe(channel string, user string, opts ...UnsubscribeOption) error {
	options := &UnsubscribeOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "unsubscribe",
		Params: unsubscribeRequest{
			Channel:            channel,
			User:               user,
			UnsubscribeOptions: *options,
		},
	}
	return p.add(cmd)
}

type disconnectRequest struct {
	User string `json:"user"`
	DisconnectOptions
}

// AddDisconnect adds disconnect command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddDisconnect(user string, opts ...DisconnectOption) error {
	options := &DisconnectOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "disconnect",
		Params: disconnectRequest{
			User:              user,
			DisconnectOptions: *options,
		},
	}
	return p.add(cmd)
}

// AddPresence adds presence command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddPresence(channel string) error {
	cmd := Command{
		Method: "presence",
		Params: map[string]interface{}{
			"channel": channel,
		},
	}
	return p.add(cmd)
}

// AddPresenceStats adds presence stats command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddPresenceStats(channel string) error {
	cmd := Command{
		Method: "presence_stats",
		Params: map[string]interface{}{
			"channel": channel,
		},
	}
	return p.add(cmd)
}

type historyRequest struct {
	Channel string `json:"channel"`
	HistoryOptions
}

// AddHistory adds history command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddHistory(channel string, opts ...HistoryOption) error {
	options := &HistoryOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "history",
		Params: historyRequest{
			Channel:        channel,
			HistoryOptions: *options,
		},
	}
	return p.add(cmd)
}

// AddHistoryRemove adds history remove command to client command buffer but not
// actually sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddHistoryRemove(channel string) error {
	cmd := Command{
		Method: "history_remove",
		Params: map[string]interface{}{
			"channel": channel,
		},
	}
	return p.add(cmd)
}

type channelsRequest struct {
	Pattern string `json:"pattern,omitempty"`
}

// AddChannels adds channels command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddChannels(opts ...ChannelsOption) error {
	options := &ChannelsOptions{}
	for _, opt := range opts {
		opt(options)
	}
	cmd := Command{
		Method: "channels",
		Params: channelsRequest{
			Pattern: options.Pattern,
		},
	}
	return p.add(cmd)
}

// AddInfo adds info command to client command buffer but not actually
// sends request to server until Pipe will be explicitly sent.
func (p *Pipe) AddInfo() error {
	cmd := Command{
		Method: "info",
		Params: map[string]interface{}{},
	}
	return p.add(cmd)
}
