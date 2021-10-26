// Package gocent is a Go language client for Centrifugo real-time messaging server HTTP API.
package gocent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	// ErrMalformedResponse can be returned when server replied with invalid response.
	ErrMalformedResponse = errors.New("malformed response returned from server")
	// ErrPipeEmpty returned when no commands found in Pipe.
	ErrPipeEmpty = errors.New("no commands in pipe")
)

// ErrStatusCode can be returned in case request to server resulted in wrong status code.
type ErrStatusCode struct {
	Code int
}

func (e ErrStatusCode) Error() string {
	return fmt.Sprintf("wrong status code: %d", e.Code)
}

// Config of client.
type Config struct {
	// Addr is Centrifugo API endpoint.
	Addr string
	// GetAddr when set will be used before every API call to extract
	// Centrifugo API endpoint. In this case Addr field of Config will be
	// ignored. Nil value means using static Config.Addr field.
	GetAddr func() (string, error)
	// Key is Centrifugo API key.
	Key string
	// HTTPClient is a custom HTTP client to be used.
	// If nil DefaultHTTPClient will be used.
	HTTPClient *http.Client
}

// Client is API client for project registered in server.
type Client struct {
	endpoint    string
	getEndpoint func() (string, error)
	apiKey      string
	httpClient  *http.Client
}

// DefaultHTTPClient will be used by default for HTTP requests.
var DefaultHTTPClient = &http.Client{Transport: &http.Transport{
	MaxIdleConnsPerHost: 100,
}, Timeout: time.Second}

// New returns initialized client instance based on provided config.
func New(c Config) *Client {
	var httpClient *http.Client
	if c.HTTPClient != nil {
		httpClient = c.HTTPClient
	} else {
		httpClient = DefaultHTTPClient
	}
	return &Client{
		endpoint:    c.Addr,
		getEndpoint: c.GetAddr,
		apiKey:      c.Key,
		httpClient:  httpClient,
	}
}

// SetHTTPClient allows to set custom http Client to use for requests. Not goroutine-safe.
func (c *Client) SetHTTPClient(httpClient *http.Client) {
	c.httpClient = httpClient
}

// Pipe allows to create new Pipe to send several commands in one HTTP request.
func (c *Client) Pipe() *Pipe {
	return &Pipe{
		commands: make([]Command, 0),
	}
}

// Publish allows to publish data to channel.
func (c *Client) Publish(ctx context.Context, channel string, data []byte, opts ...PublishOption) (PublishResult, error) {
	pipe := c.Pipe()
	err := pipe.AddPublish(channel, data, opts...)
	if err != nil {
		return PublishResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return PublishResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return PublishResult{}, resp.Error
	}
	return decodePublish(resp.Result)
}

// Broadcast allows to broadcast the same data into many channels..
func (c *Client) Broadcast(ctx context.Context, channels []string, data []byte, opts ...PublishOption) (BroadcastResult, error) {
	pipe := c.Pipe()
	err := pipe.AddBroadcast(channels, data, opts...)
	if err != nil {
		return BroadcastResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return BroadcastResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return BroadcastResult{}, resp.Error
	}
	return decodeBroadcast(resp.Result)
}

// Subscribe allow subscribing user to a channel (using server-side subscriptions).
func (c *Client) Subscribe(ctx context.Context, channel, user string, opts ...SubscribeOption) error {
	pipe := c.Pipe()
	err := pipe.AddSubscribe(channel, user, opts...)
	if err != nil {
		return err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return err
	}
	resp := result[0]
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// Unsubscribe allows to unsubscribe user from channel.
func (c *Client) Unsubscribe(ctx context.Context, channel, user string, opts ...UnsubscribeOption) error {
	pipe := c.Pipe()
	err := pipe.AddUnsubscribe(channel, user, opts...)
	if err != nil {
		return err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return err
	}
	resp := result[0]
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// Disconnect allows to close all connections of user to server.
func (c *Client) Disconnect(ctx context.Context, user string, opts ...DisconnectOption) error {
	pipe := c.Pipe()
	err := pipe.AddDisconnect(user, opts...)
	if err != nil {
		return err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return err
	}
	resp := result[0]
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// Presence returns channel presence information.
func (c *Client) Presence(ctx context.Context, channel string) (PresenceResult, error) {
	pipe := c.Pipe()
	err := pipe.AddPresence(channel)
	if err != nil {
		return PresenceResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return PresenceResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return PresenceResult{}, resp.Error
	}
	return decodePresence(resp.Result)
}

// PresenceStats returns short channel presence information (only counters).
func (c *Client) PresenceStats(ctx context.Context, channel string) (PresenceStatsResult, error) {
	pipe := c.Pipe()
	err := pipe.AddPresenceStats(channel)
	if err != nil {
		return PresenceStatsResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return PresenceStatsResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return PresenceStatsResult{}, resp.Error
	}
	return decodePresenceStats(resp.Result)
}

// History returns channel history.
func (c *Client) History(ctx context.Context, channel string, opts ...HistoryOption) (HistoryResult, error) {
	pipe := c.Pipe()
	err := pipe.AddHistory(channel, opts...)
	if err != nil {
		return HistoryResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return HistoryResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return HistoryResult{}, resp.Error
	}
	return decodeHistory(resp.Result)
}

// HistoryRemove removes channel history.
func (c *Client) HistoryRemove(ctx context.Context, channel string) error {
	pipe := c.Pipe()
	err := pipe.AddHistoryRemove(channel)
	if err != nil {
		return err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return err
	}
	resp := result[0]
	if resp.Error != nil {
		return resp.Error
	}
	return nil
}

// Channels returns information about active channels (with one or more subscribers) on server.
func (c *Client) Channels(ctx context.Context, opts ...ChannelsOption) (ChannelsResult, error) {
	pipe := c.Pipe()
	err := pipe.AddChannels(opts...)
	if err != nil {
		return ChannelsResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return ChannelsResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return ChannelsResult{}, resp.Error
	}
	return decodeChannels(resp.Result)
}

// Info returns information about server nodes.
func (c *Client) Info(ctx context.Context) (InfoResult, error) {
	pipe := c.Pipe()
	err := pipe.AddInfo()
	if err != nil {
		return InfoResult{}, err
	}
	result, err := c.SendPipe(ctx, pipe)
	if err != nil {
		return InfoResult{}, err
	}
	resp := result[0]
	if resp.Error != nil {
		return InfoResult{}, resp.Error
	}
	return decodeInfo(resp.Result)
}

func decodePublish(result []byte) (PublishResult, error) {
	var r PublishResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return PublishResult{}, err
	}
	return r, nil
}

func decodeBroadcast(result []byte) (BroadcastResult, error) {
	var r BroadcastResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return BroadcastResult{}, err
	}
	return r, nil
}

// decodeHistory allows to decode history reply result to get a slice of messages.
func decodeHistory(result []byte) (HistoryResult, error) {
	var r HistoryResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return HistoryResult{}, err
	}
	return r, nil
}

// decodeChannels allows to decode channels command reply result to get a slice of channels.
func decodeChannels(result []byte) (ChannelsResult, error) {
	var r ChannelsResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return ChannelsResult{}, err
	}
	return r, nil
}

// decodeInfo allows to decode info command response result.
func decodeInfo(result []byte) (InfoResult, error) {
	var info InfoResult
	err := json.Unmarshal(result, &info)
	if err != nil {
		return InfoResult{}, err
	}
	return info, nil
}

// decodePresence allows to decode presence reply result to get a map of clients.
func decodePresence(result []byte) (PresenceResult, error) {
	var r PresenceResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return PresenceResult{}, err
	}
	return r, nil
}

// decodePresenceStats allows to decode presence stats reply result to get a map of clients.
func decodePresenceStats(result []byte) (PresenceStatsResult, error) {
	var r PresenceStatsResult
	err := json.Unmarshal(result, &r)
	if err != nil {
		return PresenceStatsResult{}, err
	}
	return r, nil
}

// SendPipe sends Commands collected in Pipe to Centrifugo. Using this method you
// should manually inspect all replies.
func (c *Client) SendPipe(ctx context.Context, pipe *Pipe) ([]Reply, error) {
	if len(pipe.commands) == 0 {
		return nil, ErrPipeEmpty
	}
	result, err := c.send(ctx, pipe.commands)
	if err != nil {
		return nil, err
	}
	if len(result) != len(pipe.commands) {
		return nil, ErrMalformedResponse
	}
	return result, nil
}

func (c *Client) send(ctx context.Context, commands []Command) ([]Reply, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)

	for _, cmd := range commands {
		err := enc.Encode(cmd)
		if err != nil {
			return nil, err
		}
	}

	var endpoint string

	if c.getEndpoint != nil {
		e, err := c.getEndpoint()
		if err != nil {
			return nil, err
		}
		endpoint = e
	} else {
		endpoint = c.endpoint
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)

	if c.apiKey != "" {
		req.Header.Set("Authorization", "apikey "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, ErrStatusCode{resp.StatusCode}
	}

	var replies []Reply

	dec := json.NewDecoder(resp.Body)
	for {
		var rep Reply
		if err := dec.Decode(&rep); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		replies = append(replies, rep)
	}

	return replies, err
}
