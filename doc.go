// The MIT License (MIT)
//
// Copyright (c) 2015, Alexandr Emelin

// Package gocent is a Go language client for Centrifugo real-time messaging server.
//
// Usage example
//
// In example below we initialize new client with server URL address, project key, project
// secret and request timeout. Then publish data into channel, call presence and history
// for channel and finally show how to publish several messages in one POST request to API
// endpoint using internal command buffer.
//
//  c := NewClient("http://localhost:8000", "development", "secret", 5*time.Second)
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

package gocent
