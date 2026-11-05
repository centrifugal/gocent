package gocent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Request is implemented by the request type of every API method:
// [PublishRequest], [BroadcastRequest] and the others - as values and as
// pointers. Only gocent's types implement it.
type Request interface {
	// APIMethod returns the API method the request is for, such as
	// "publish".
	APIMethod() string
	asRequest() request
}

// Command is an API call left for Centrifugo's async consumers to make,
// instead of calling Centrifugo: written to a PostgreSQL outbox table, a
// Kafka topic, a Redis stream. See
// https://centrifugal.dev/docs/server/consumers.
//
// Consumers take a command in one of two forms:
//
//   - its Method and Payload apart: the outbox table's method and payload
//     columns, or a payload with the method in a message attribute or field
//     (Google Cloud PUB/SUB, AWS SQS, Azure Service Bus, Redis Stream);
//   - a Command encoded as JSON, {"method": ..., "payload": ...}, as the
//     message itself (Kafka, Nats Jetstream, and the others when the method
//     is not given apart).
//
// Within a partition - an outbox table's partition, a Kafka partition - a
// consumer runs commands in order; across partitions it does not. To keep the
// commands of a channel in order, write them to one partition, derived from
// the channel the same way by every writer: the channel as a Kafka message
// key, say, or a hash of it modulo the outbox table's number of partitions.
type Command struct {
	// Method is the API method, such as "publish".
	Method string `json:"method"`
	// Payload is the request, as JSON.
	Payload jsontext.Value `json:"payload"`
}

// NewCommand returns the command making the API call req: it checks req as
// the Client does before sending it - an invalid request fails here, wrapping
// [ErrInvalidRequest], rather than in Centrifugo, whose consumers drop a
// command they cannot run - and encodes it.
//
//	cmd, err := gocent.NewCommand(gocent.PublishRequest{Channel: "news", Data: data})
//	if err != nil {
//		return err
//	}
//	_, err = tx.ExecContext(ctx,
//		"INSERT INTO centrifugo_outbox (method, payload, partition) VALUES ($1, $2, 0)",
//		cmd.Method, cmd.Payload)
func NewCommand(req Request) (Command, error) {
	r := req.asRequest()
	if err := validateRequest(r); err != nil {
		return Command{}, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return Command{}, encodingError(r.APIMethod(), err)
	}
	return Command{Method: r.APIMethod(), Payload: payload}, nil
}
