package gocent

import "testing"

func TestNewClient(t *testing.T) {
	c := New(Config{})
	if c == nil {
		t.Errorf("New returned nil client")
	}
}

func TestNewClientChannels(t *testing.T) {
	c := New(Config{
		Addr: "http://localhost:8000/api",
		Key:  "<API key>",
	})
	if c == nil {
		t.Fatalf("New returned nil client")
	}
	channels, err := c.Channels(t.Context())
	if err != nil {
		t.Fatalf("err:%v", err)
	}
	for name, channel := range channels.Channels {
		t.Logf("name:%v,channel:%+v", name, channel)
	}
}
