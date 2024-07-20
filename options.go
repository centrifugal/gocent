package gocent

import "encoding/json"

type PublishOptions struct {
	SkipHistory bool `json:"skip_history,omitempty"`
}

// PublishOption is a type to represent various Publish options.
type PublishOption func(*PublishOptions)

// WithSkipHistory allows to set SkipHistory field.
func WithSkipHistory(skip bool) PublishOption {
	return func(opts *PublishOptions) {
		opts.SkipHistory = skip
	}
}

// SubscribeOptions define per-subscription options.
type SubscribeOptions struct {
	// ChannelInfo defines custom channel information, zero value means no channel information.
	Info json.RawMessage `json:"info,omitempty"`
	// Presence turns on participating in channel presence.
	Presence bool `json:"presence,omitempty"`
	// JoinLeave enables sending Join and Leave messages for this client in channel.
	JoinLeave bool `json:"join_leave,omitempty"`
	// When position is on client will additionally sync its position inside
	// a stream to prevent message loss. Make sure you are enabling Position in channels
	// that maintain Publication history stream. When Position is on  Centrifuge will
	// include StreamPosition information to subscribe response - for a client to be able
	// to manually track its position inside a stream.
	Position bool `json:"position,omitempty"`
	// Recover turns on recovery option for a channel. In this case client will try to
	// recover missed messages automatically upon resubscribe to a channel after reconnect
	// to a server. This option also enables client position tracking inside a stream
	// (like Position option) to prevent occasional message loss. Make sure you are using
	// Recover in channels that maintain Publication history stream.
	Recover bool `json:"recover,omitempty"`
	// Data to send to a client with Subscribe Push.
	Data json.RawMessage `json:"data,omitempty"`
	// RecoverSince will try to subscribe a client and recover from a certain StreamPosition.
	RecoverSince *StreamPosition `json:"recover_since,omitempty"`
	// ClientID to subscribe.
	ClientID string `json:"client,omitempty"`
}

// SubscribeOption is a type to represent various Subscribe options.
type SubscribeOption func(*SubscribeOptions)

// WithSubscribeInfo ...
func WithSubscribeInfo(chanInfo json.RawMessage) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.Info = chanInfo
	}
}

// WithPresence ...
func WithPresence(enabled bool) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.Presence = enabled
	}
}

// WithJoinLeave ...
func WithJoinLeave(enabled bool) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.JoinLeave = enabled
	}
}

// WithPosition ...
func WithPosition(enabled bool) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.Position = enabled
	}
}

// WithRecover ...
func WithRecover(enabled bool) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.Recover = enabled
	}
}

// WithSubscribeClient allows setting client ID that should be subscribed.
// This option not used when Client.Subscribe called.
func WithSubscribeClient(clientID string) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.ClientID = clientID
	}
}

// WithSubscribeData allows setting custom data to send with subscribe push.
func WithSubscribeData(data json.RawMessage) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.Data = data
	}
}

// WithRecoverSince allows setting SubscribeOptions.RecoverFrom.
func WithRecoverSince(since *StreamPosition) SubscribeOption {
	return func(opts *SubscribeOptions) {
		opts.RecoverSince = since
	}
}

// UnsubscribeOptions ...
type UnsubscribeOptions struct {
	// ClientID to unsubscribe.
	ClientID string `json:"client,omitempty"`
}

// UnsubscribeOption is a type to represent various Unsubscribe options.
type UnsubscribeOption func(options *UnsubscribeOptions)

// WithUnsubscribeClient allows setting client ID that should be unsubscribed.
// This option not used when Client.Unsubscribe called.
func WithUnsubscribeClient(clientID string) UnsubscribeOption {
	return func(opts *UnsubscribeOptions) {
		opts.ClientID = clientID
	}
}

// Disconnect allows to configure how client will be disconnected from server.
// The important note that Disconnect serialized to JSON must be less than 127 bytes
// due to WebSocket protocol limitations (because at moment we send Disconnect inside
// reason field of WebSocket close handshake).
type Disconnect struct {
	// Code is disconnect code.
	Code uint32 `json:"code,omitempty"`
	// Reason is a short description of disconnect.
	Reason string `json:"reason"`
	// Reconnect gives client an advice to reconnect after disconnect or not.
	Reconnect bool `json:"reconnect"`
}

// DisconnectOptions define some fields to alter behaviour of Disconnect operation.
type DisconnectOptions struct {
	// Disconnect represents custom disconnect to use.
	// By default DisconnectForceNoReconnect will be used.
	Disconnect *Disconnect
	// ClientWhitelist contains client IDs to keep.
	ClientWhitelist []string
	// ClientID to disconnect.
	ClientID string `json:"client,omitempty"`
}

// DisconnectOption is a type to represent various Disconnect options.
type DisconnectOption func(options *DisconnectOptions)

// WithDisconnect allows to set custom Disconnect.
func WithDisconnect(disconnect *Disconnect) DisconnectOption {
	return func(opts *DisconnectOptions) {
		opts.Disconnect = disconnect
	}
}

// WithDisconnectClient allows to set DisconnectOptions.ClientID.
func WithDisconnectClient(clientID string) DisconnectOption {
	return func(opts *DisconnectOptions) {
		opts.ClientID = clientID
	}
}

// WithDisconnectClientWhitelist allows to set DisconnectOptions.ClientWhitelist.
func WithDisconnectClientWhitelist(whitelist []string) DisconnectOption {
	return func(opts *DisconnectOptions) {
		opts.ClientWhitelist = whitelist
	}
}

// HistoryOptions define some fields to alter History method behaviour.
type HistoryOptions struct {
	// Since used to extract publications from stream since provided StreamPosition.
	Since *StreamPosition `json:"since,omitempty"`
	// Limit number of publications to return.
	// -1 means no limit - i.e. return all publications currently in stream.
	// 0 means that caller only interested in current stream top position so
	// Broker should not return any publications in result.
	// Positive integer does what it should.
	Limit int `json:"limit,omitempty"`
	// Reverse direction.
	Reverse bool `json:"reverse,omitempty"`
}

// HistoryOption is a type to represent various History options.
type HistoryOption func(options *HistoryOptions)

// NoLimit defines that limit should not be applied.
const NoLimit = -1

// WithLimit allows to set HistoryOptions.Limit.
func WithLimit(limit int) HistoryOption {
	return func(opts *HistoryOptions) {
		opts.Limit = limit
	}
}

// StreamPosition contains fields to describe position in stream.
// At moment this is used for automatic recovery mechanics. More info about stream
// recovery in docs: https://centrifugal.github.io/centrifugo/server/recover/.
type StreamPosition struct {
	// Offset defines publication incremental offset inside a stream.
	Offset uint64 `json:"offset,omitempty"`
	// Epoch allows handling situations when storage
	// lost stream entirely for some reason (expired or lost after restart) and we
	// want to track this fact to prevent successful recovery from another stream.
	// I.e. for example we have stream [1, 2, 3], then it's lost and new stream
	// contains [1, 2, 3, 4], client that recovers from position 3 will only receive
	// publication 4 missing 1, 2, 3 from new stream. With epoch we can tell client
	// that correct recovery is not possible.
	Epoch string `json:"epoch,omitempty"`
}

// WithSince allows to set HistoryOptions.Since option.
func WithSince(sp *StreamPosition) HistoryOption {
	return func(opts *HistoryOptions) {
		opts.Since = sp
	}
}

// WithReverse allows to set HistoryOptions.Reverse option.
func WithReverse(reverse bool) HistoryOption {
	return func(opts *HistoryOptions) {
		opts.Reverse = reverse
	}
}

// ChannelsOptions define some fields to alter Channels method behaviour.
type ChannelsOptions struct {
	// Pattern to filter channels.
	Pattern string `json:"pattern,omitempty"`
}

// ChannelsOption is a type to represent various Channels call options.
type ChannelsOption func(options *ChannelsOptions)

// WithPattern allows to set ChannelsOptions.Pattern.
func WithPattern(pattern string) ChannelsOption {
	return func(opts *ChannelsOptions) {
		opts.Pattern = pattern
	}
}
