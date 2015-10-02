// The MIT License (MIT)
//
// Copyright (c) 2015, Alexandr Emelin

// Package gocent is a Go language API client for Centrifugo real-time messaging server.
//
// Usage example
//
// In example below we initialize new client with server URL address, project key, project
// secret and request timeout. Then publish data into channel, call presence and history
// for channel and finally show how to publish several messages in one POST request to API
// endpoint using internal command buffer.
//
//  c := NewClient("http://localhost:8000", "secret", 5*time.Second)
//  ok, err := c.Publish("$public:chat", []byte(`{"input": "test"}`))
//  if err != nil {
//  	println(err.Error())
//  	return
//  }
//  println(ok)
//  presence, _ := c.Presence("$public:chat")
//  fmt.Printf("%v", presence)
//  history, _ := c.History("$public:chat")
//  fmt.Printf("%v", history)
//  _ = c.AddPublish("$public:chat", []byte(`{"input": "test1"}`))
//  _ = c.AddPublish("$public:chat", []byte(`{"input": "test2"}`))
//  _ = c.AddPublish("$public:chat", []byte(`{"input": "test3"}`))
//  result, err := c.Send()
//  println(len(result))
//
package gocent // import "github.com/centrifugal/gocent"

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifugo/libcentrifugo"
	"github.com/centrifugal/centrifugo/libcentrifugo/auth"
)

var (
	// ErrClientNotEmpty can be returned when client with non empty commands buffer
	// is used for single command send.
	ErrClientNotEmpty = errors.New("client command buffer not empty, send commands or reset client")
	// ErrMalformedResponse can be returned when server replied with invalid response.
	ErrMalformedResponse = errors.New("malformed response returned from server")
)

// Client is API client for project registered in server.
type Client struct {
	sync.RWMutex

	Endpoint string
	Secret   string
	Timeout  time.Duration
	cmds     []Command
}

// Command represents API command to send.
type Command struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

// Response is a response of server on command sent.
type Response struct {
	Method string
	Error  string
	Body   json.RawMessage
}

// Result is a slice of responses.
type Result []Response

// NewClient returns initialized client instance based on provided server address,
//project key, project secret and timeout.
func NewClient(addr, secret string, timeout time.Duration) *Client {

	addr = strings.TrimRight(addr, "/")
	if !strings.HasSuffix(addr, "/api") {
		addr = addr + "/api"
	}

	apiEndpoint := addr + "/"

	return &Client{
		Endpoint: apiEndpoint,
		Secret:   secret,
		Timeout:  timeout,
		cmds:     []Command{},
	}
}

func (c *Client) empty() bool {
	c.RLock()
	defer c.RUnlock()
	return len(c.cmds) == 0
}

// Reset allows to clear client command buffer.
func (c *Client) Reset() {
	c.Lock()
	defer c.Unlock()
	c.cmds = []Command{}
}

// AddPublish adds publish command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddPublish(channel string, data []byte) error {
	c.Lock()
	defer c.Unlock()
	var raw json.RawMessage
	raw = json.RawMessage(data)
	cmd := Command{
		Method: "publish",
		Params: map[string]interface{}{
			"channel": channel,
			"data":    &raw,
		},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddUnsubscribe adds unsubscribe command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddUnsubscribe(channel string, user string) error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "unsubscribe",
		Params: map[string]interface{}{
			"channel": channel,
			"user":    user,
		},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddDisconnect adds disconnect command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddDisconnect(user string) error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "disconnect",
		Params: map[string]interface{}{
			"user": user,
		},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddPresence adds presence command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddPresence(channel string) error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "presence",
		Params: map[string]interface{}{
			"channel": channel,
		},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddHistory adds history command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddHistory(channel string) error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "history",
		Params: map[string]interface{}{
			"channel": channel,
		},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddChannels adds channels command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddChannels() error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "channels",
		Params: map[string]string{},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// AddStats adds stats command to client command buffer but not actually
// send it until Send method explicitly called.
func (c *Client) AddStats() error {
	c.Lock()
	defer c.Unlock()
	cmd := Command{
		Method: "stats",
		Params: map[string]string{},
	}
	c.cmds = append(c.cmds, cmd)
	return nil
}

// Publish sends publish command to server and returns boolean indicator of success and
// any error occurred in process.
func (c *Client) Publish(channel string, data []byte) (bool, error) {
	if !c.empty() {
		return false, ErrClientNotEmpty
	}
	c.AddPublish(channel, data)
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return false, err
	}
	resp := result[0]
	if resp.Error != "" {
		return false, errors.New(resp.Error)
	}
	return DecodePublish(resp.Body)
}

// Unsubscribe sends unsubscribe command to server and returns boolean indicator of success and
// any error occurred in process.
func (c *Client) Unsubscribe(channel, user string) (bool, error) {
	if !c.empty() {
		return false, ErrClientNotEmpty
	}
	c.AddUnsubscribe(channel, user)
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return false, err
	}
	resp := result[0]
	if resp.Error != "" {
		return false, errors.New(resp.Error)
	}
	return DecodeUnsubscribe(resp.Body)
}

// Disconnect sends disconnect command to server and returns boolean indicator of success and
// any error occurred in process.
func (c *Client) Disconnect(user string) (bool, error) {
	if !c.empty() {
		return false, ErrClientNotEmpty
	}
	c.AddDisconnect(user)
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return false, err
	}
	resp := result[0]
	if resp.Error != "" {
		return false, errors.New(resp.Error)
	}
	return DecodeDisconnect(resp.Body)
}

// Presence sends presence command for channel to server and returns map with client
// information and any error occurred in process.
func (c *Client) Presence(channel string) (map[libcentrifugo.ConnID]libcentrifugo.ClientInfo, error) {
	if !c.empty() {
		return map[libcentrifugo.ConnID]libcentrifugo.ClientInfo{}, ErrClientNotEmpty
	}
	c.AddPresence(channel)
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return map[libcentrifugo.ConnID]libcentrifugo.ClientInfo{}, err
	}
	resp := result[0]
	if resp.Error != "" {
		return map[libcentrifugo.ConnID]libcentrifugo.ClientInfo{}, errors.New(resp.Error)
	}
	return DecodePresence(resp.Body)
}

// History sends history command for channel to server and returns slice with
// messages and any error occurred in process.
func (c *Client) History(channel string) ([]libcentrifugo.Message, error) {
	if !c.empty() {
		return []libcentrifugo.Message{}, ErrClientNotEmpty
	}
	c.AddHistory(channel)
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return []libcentrifugo.Message{}, err
	}
	resp := result[0]
	if resp.Error != "" {
		return []libcentrifugo.Message{}, errors.New(resp.Error)
	}
	return DecodeHistory(resp.Body)
}

// Channels sends channels command to server and returns slice with
// active channels (with one or more subscribers).
func (c *Client) Channels() ([]libcentrifugo.Channel, error) {
	if !c.empty() {
		return []libcentrifugo.Channel{}, ErrClientNotEmpty
	}
	c.AddChannels()
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return []libcentrifugo.Channel{}, err
	}
	resp := result[0]
	if resp.Error != "" {
		return []libcentrifugo.Channel{}, errors.New(resp.Error)
	}
	return DecodeChannels(resp.Body)
}

// Stats sends stats command to server and returns libcentrifugo.Stats.
func (c *Client) Stats() ([]libcentrifugo.Stats, error) {
	if !c.empty() {
		return libcentrifugo.Stats{}, ErrClientNotEmpty
	}
	c.AddStats()
	c.Lock()
	defer c.Unlock()
	result, err := c.Send()
	if err != nil {
		return libcentrifugo.Stats{}, err
	}
	resp := result[0]
	if resp.Error != "" {
		return libcentrifugo.Stats{}, errors.New(resp.Error)
	}
	return DecodeStats(resp.Body)
}

// DecodePublish allows to decode response body of publish command to get
// success flag from it. Currently no error in response means success - so nothing
// to do here yet.
func DecodePublish(body []byte) (bool, error) {
	return true, nil
}

// DecodeUnsubscribe allows to decode response body of unsubscribe command to get
// success flag from it. Currently no error in response means success - so nothing
// to do here yet.
func DecodeUnsubscribe(body []byte) (bool, error) {
	return true, nil
}

// DecodeDisconnect allows to decode response body of disconnect command to get
// success flag from it. Currently no error in response means success - so nothing
// to do here yet.
func DecodeDisconnect(body []byte) (bool, error) {
	return true, nil
}

// DecodeHistory allows to decode history response body to get a slice of messages.
func DecodeHistory(body []byte) ([]libcentrifugo.Message, error) {
	var d libcentrifugo.HistoryBody
	err := json.Unmarshal(body, &d)
	if err != nil {
		return []libcentrifugo.Message{}, err
	}
	return d.Data, nil
}

// DecodeChannels allows to decode channels command response body to get a slice of channels.
func DecodeChannels(body []byte) ([]libcentrifugo.Channel, error) {
	var d libcentrifugo.ChannelsBody
	err := json.Unmarshal(body, &d)
	if err != nil {
		return []libcentrifugo.Channel{}, err
	}
	return d.Data, nil
}

// DecodeStats allows to decode stats command response body.
func DecodeStats(body []byte) (libcentrifugo.Stats, error) {
	var d libcentrifugo.StatsBody
	err := json.Unmarshal(body, &d)
	if err != nil {
		return libcentrifugo.Stats{}, err
	}
	return d.Data, nil
}

// DecodePresence allows to decode presence response body to get a map of clients.
func DecodePresence(body []byte) (map[libcentrifugo.ConnID]libcentrifugo.ClientInfo, error) {
	var d libcentrifugo.PresenceBody
	err := json.Unmarshal(body, &d)
	if err != nil {
		return map[libcentrifugo.ConnID]libcentrifugo.ClientInfo{}, err
	}
	return d.Data, nil
}

// Send actually makes API POST request to server sending all buffered commands in
// one request. Using this method you should manually decode all responses in
// returned Result.
func (c *Client) Send() (Result, error) {
	cmds := c.cmds
	c.cmds = []Command{}
	result, err := c.send(cmds)
	if err != nil {
		return Result{}, err
	}
	if len(result) != len(cmds) {
		return Result{}, ErrMalformedResponse
	}
	return result, nil
}

func (c *Client) send(cmds []Command) (Result, error) {
	data, err := json.Marshal(cmds)
	if err != nil {
		return Result{}, err
	}

	client := &http.Client{}
	client.Timeout = c.Timeout
	r, err := http.NewRequest("POST", c.Endpoint, bytes.NewBuffer(data))
	if err != nil {
		return Result{}, err
	}

	r.Header.Set("X-API-Sign", auth.GenerateApiSign(c.Secret, data))
	r.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(r)
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, errors.New("wrong status code: " + resp.Status)
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)

	var result Result
	err = json.Unmarshal(body, &result)
	return result, err
}
