package main

import (
	"fmt"
	"time"

	"github.com/centrifugal/gocent"
)

func main() {

	ch := "$public:chat"

	c := gocent.NewClient("http://localhost:8000", "secret", 5*time.Second)

	// How to publish.
	ok, err := c.Publish(ch, []byte(`{"input": "test"}`))
	if err != nil {
		println(err.Error())
		return
	}
	println("Publish successful:", ok)

	// How to get presence.
	presence, _ := c.Presence(ch)
	fmt.Printf("Presense: %v\n", presence)

	// How to get history.
	history, _ := c.History(ch)
	fmt.Printf("History: %v\n", history)

	// How to get channels.
	channels, _ := c.Channels()
	fmt.Printf("Channels: %v\n", channels)

	// How to export stats.
	stats, _ := c.Stats()
	fmt.Printf("Stats: %v\n", stats)

	// How to send 3 commands in ome request.
	_ = c.AddPublish(ch, []byte(`{"input": "test1"}`))
	_ = c.AddPublish(ch, []byte(`{"input": "test2"}`))
	_ = c.AddPublish(ch, []byte(`{"input": "test3"}`))
	result, err := c.Send()
	println("Sent", len(result), "publish commands in one request")

	// How to broadcast the same data into 3 different channels.
	chs := []string{"$public:chat_1", "$public:chat_2", "$public:chat_3"}
	ok, err = c.Broadcast(chs, []byte(`{"input": "test"}`))
	if err != nil {
		println(err.Error())
		return
	}
	println("Broadcast successful:", ok)
}
