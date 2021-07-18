package main

import (
	"context"
	"log"

	"github.com/centrifugal/gocent/v3"
)

func main() {
	c := gocent.New(gocent.Config{
		Addr: "http://localhost:8000/api",
		Key:  "<API key>",
	})

	ch := "$chat:index"
	ctx := context.Background()

	result, err := c.Publish(ctx, ch, []byte(`{"input": "test"}`))
	if err != nil {
		log.Fatalf("Error calling publish: %v", err)
	}
	log.Printf("Publish into channel %s successful, stream position {offset: %d, epoch: %s}", ch, result.Offset, result.Epoch)

	// How to get presence.
	presenceResult, err := c.Presence(ctx, ch)
	if err != nil {
		log.Fatalf("Error calling presence: %v", err)
	}
	log.Printf("Presense for channel %s: %d active subscribers", ch, len(presenceResult.Presence))

	// How to get presence stats.
	presenceStatsResult, err := c.PresenceStats(ctx, ch)
	if err != nil {
		log.Fatalf("Error calling presence: %v", err)
	}
	log.Printf("Presense stats for channel %s: %d unique users, %d total subscribers", ch, presenceStatsResult.NumUsers, presenceStatsResult.NumClients)

	// How to get history.
	historyResult, err := c.History(ctx, ch)
	if err != nil {
		log.Fatalf("Error calling history: %v", err)
	}
	log.Printf("History for channel %s, %d messages", ch, len(historyResult.Publications))

	// How to get channels.
	channelsResult, err := c.Channels(ctx)
	if err != nil {
		log.Fatalf("Error calling channels: %v", err)
	}
	log.Printf("Channels: %#v", channelsResult.Channels)

	// Get info about nodes.
	info, err := c.Info(ctx)
	if err != nil {
		log.Fatalf("Error calling info: %v", err)
	}
	log.Printf("Info: %d Centrifugo nodes running", len(info.Nodes))

	// How to broadcast the same data into 3 different channels in one request.
	chs := []string{"chat_1", "chat_2", "chat_3"}
	_, err = c.Broadcast(ctx, chs, []byte(`{"input": "test"}`))
	if err != nil {
		log.Fatalf("Error calling broadcast: %v", err)
	}
	log.Printf("Broadcast to %d channels is successful", len(chs))

	// How to remove history.
	err = c.HistoryRemove(ctx, ch)
	if err != nil {
		log.Fatalf("Error calling history remove: %v", err)
	}
	log.Print("History for channel removed")

	// How to send 3 commands in one request.
	pipe := c.Pipe()
	_ = pipe.AddPublish(ch, []byte(`{"input": "test1"}`))
	_ = pipe.AddPublish(ch, []byte(`{"input": "test2"}`))
	_ = pipe.AddPublish(ch, []byte(`{"input": "test3"}`))
	replies, err := c.SendPipe(ctx, pipe)
	if err != nil {
		log.Fatalf("Error sending pipe: %v", err)
	}
	for _, reply := range replies {
		if reply.Error != nil {
			log.Fatalf("Error in pipe reply: %v", err)
		}
	}
	log.Printf("Sent %d publish commands in one HTTP request ", len(replies))

}
