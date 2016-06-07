package gocent

import (
	"encoding/json"
)

// Command represents API command to send.
type Command struct {
	UID    string                 `json:"uid"`
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

// Response is a response of server on command sent.
type Response struct {
	Method string          `json:"method"`
	Error  string          `json:"error"`
	Body   json.RawMessage `json:"body"`
}

// Result is a slice of responses.
type Result []Response

// ClientInfo represents information about one client connection to Centrifugo.
// This struct used in messages published by clients, join/leave events, presence data.
type ClientInfo struct {
	User        string           `json:"user"`
	Client      string           `json:"client"`
	DefaultInfo *json.RawMessage `json:"default_info,omitempty"`
	ChannelInfo *json.RawMessage `json:"channel_info,omitempty"`
}

// Message represents message published into channel.
type Message struct {
	UID       string           `json:"uid"`
	Timestamp string           `json:"timestamp"`
	Info      *ClientInfo      `json:"info,omitempty"`
	Channel   string           `json:"channel"`
	Data      *json.RawMessage `json:"data"`
	Client    string           `json:"client,omitempty"`
}

// NodeInfo contains information and statistics about Centrifugo node.
type NodeInfo struct {
	// UID is a unique id of running node.
	UID string `json:"uid"`
	// Name is a name of node (config defined or generated automatically).
	Name string `json:"name"`
	// Goroutines is a number of current running goroutines.
	Goroutines int `json:"num_goroutine"`
	// Clients is how many clients currently connected to node.
	Clients int `json:"num_clients"`
	// Unique shows how many clients are unique (different user ID).
	Unique int `json:"num_unique_clients"`
	// Channels shows how many different channels exist at moment.
	Channels int `json:"num_channels"`
	// Started is node start timestamp.
	Started int64 `json:"started_at"`
	// Gomaxprocs shows how many CPUs node process using.
	Gomaxprocs int `json:"gomaxprocs"`
	// NumCPU is total CPU number on machine running node.
	NumCPU int `json:"num_cpu"`
	// NumMsgPublished is how many messages were published into channels.
	NumMsgPublished int64 `json:"num_msg_published"`
	// NumMsgQueued is how many messages were put into client queues.
	NumMsgQueued int64 `json:"num_msg_queued"`
	// NumMsgSent is how many messages were actually sent into client connections.
	NumMsgSent int64 `json:"num_msg_sent"`
	// NumAPIRequests shows amount of requests to server API.
	NumAPIRequests int64 `json:"num_api_requests"`
	// NumClientRequests shows amount of requests to client API.
	NumClientRequests int64 `json:"num_client_requests"`
	// BytesClientIn shows amount of data in bytes coming into client API.
	BytesClientIn int64 `json:"bytes_client_in"`
	// BytesClientOut shows amount of data in bytes coming out if client API.
	BytesClientOut int64 `json:"bytes_client_out"`
	// TimeAPIMean shows mean response time in nanoseconds to API requests. DEPRECATED!
	TimeAPIMean int64 `json:"time_api_mean"`
	// TimeClientMean shows mean response time in nanoseconds to client requests. DEPRECATED!
	TimeClientMean int64 `json:"time_client_mean"`
	// TimeAPIMax shows maximum response time to API request. DEPRECATED!
	TimeAPIMax int64 `json:"time_api_max"`
	// TimeClientMax shows maximum response time to client request. DEPRECATED!
	TimeClientMax int64 `json:"time_client_max"`
	// MemSys shows system memory usage in bytes.
	MemSys int64 `json:"memory_sys"`
	// CPU shows cpu usage (actually just a snapshot value) in percents.
	CPU int64 `json:"cpu_usage"`
}

// Stats contains state and metrics information from all running Centrifugo nodes.
type Stats struct {
	Nodes           []NodeInfo `json:"nodes"`
	MetricsInterval int64      `json:"metrics_interval"`
}

// presenceBody represents body of response in case of successful presence command.
type presenceBody struct {
	Channel string                `json:"channel"`
	Data    map[string]ClientInfo `json:"data"`
}

// historyBody represents body of response in case of successful history command.
type historyBody struct {
	Channel string    `json:"channel"`
	Data    []Message `json:"data"`
}

// channelsBody represents body of response in case of successful channels command.
type channelsBody struct {
	Data []string `json:"data"`
}

// statsBody represents body of response in case of successful stats command.
type statsBody struct {
	Data Stats `json:"data"`
}
