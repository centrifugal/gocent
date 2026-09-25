package gocent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Batch collects commands and sends them to Centrifugo in one request. Create
// one with [Client.NewBatch], add commands with its methods - one per API
// method, such as [Batch.Publish] - then call [Batch.Send]:
//
//	b := client.NewBatch()
//	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
//	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
//	if err := b.Send(ctx, gocent.BatchOptions{}); err != nil {
//		// See Send for what the error tells you.
//	}
//	res, err := pub.Result()
//
// Each method returns a [Pending] holding that command's outcome once the
// batch is sent. A Batch is sent once, and is not safe for concurrent use.
type Batch struct {
	client   *Client
	commands []command
	methods  []string
	// checks holds, for commands which can fail in part - a broadcast - how
	// to tell from a successful reply that a part of it failed.
	checks []func(*reply) error
	// invalid holds, per command, what the client found wrong with it.
	invalid []error
	replies []reply
	err     error
	sent    bool
}

// BatchOptions controls how Centrifugo runs a batch.
type BatchOptions struct {
	// Parallel lets Centrifugo run the commands concurrently. It is faster
	// for many independent commands, but the commands no longer take effect
	// in the order they were added.
	Parallel bool

	// GroupPublications asks Centrifugo PRO to send the batch's publications
	// to its broker together. A channel's own publications keep their order,
	// but publications into different channels may take effect in a
	// different order, and an error for a group is reported to every
	// publication in it. Centrifugo OSS ignores it. See
	// [AutoBatch.GroupPublications].
	GroupPublications bool
}

// ErrBatchNotSent is returned by [Pending.Result] before its batch is sent.
var ErrBatchNotSent = errors.New("gocent: batch not sent yet")

// ErrBatchSent is returned by [Batch.Send] for a batch already sent.
var ErrBatchSent = errors.New("gocent: batch already sent")

// NewBatch returns an empty [Batch] sent by c.
func (c *Client) NewBatch() *Batch {
	return &Batch{client: c}
}

// Len returns the number of commands in the batch.
func (b *Batch) Len() int { return len(b.commands) }

// Send sends the batch and waits for Centrifugo's reply to every command.
//
//   - nil means every command succeeded.
//   - A [*BatchError] means one or more commands failed, up to all of them;
//     every [Pending] holds its own command's outcome, success or failure.
//   - Any other error means no reply came back - the request failed as a
//     whole - and every Pending returns that same error.
//
// Sending an empty batch does nothing and returns nil. A batch is sent once;
// sending it again returns [ErrBatchSent].
func (b *Batch) Send(ctx context.Context, opts BatchOptions) error {
	if b.sent {
		return ErrBatchSent
	}
	b.sent = true
	if len(b.commands) == 0 {
		return nil
	}
	// A command the client can tell is invalid would fail the whole batch
	// when encoded - or reach Centrifugo only to be refused - so nothing is
	// sent, and the error names the command.
	for i, err := range b.invalid {
		if err != nil {
			b.err = fmt.Errorf("batch command #%d: %w", i, err)
			return b.err
		}
	}
	replies, err := b.client.sendBatch(ctx, b.commands, opts)
	if err != nil {
		b.err = err
		return err
	}
	b.replies = replies

	var be *BatchError
	for i := range replies {
		var err error
		switch {
		case replies[i].Error != nil:
			err = replies[i].Error
		case b.checks[i] != nil:
			err = b.checks[i](&replies[i])
		}
		if err == nil {
			continue
		}
		if be == nil {
			be = &BatchError{Total: len(replies)}
		}
		be.Failed = append(be.Failed, CommandError{Index: i, Method: b.methods[i], Err: err})
	}
	if be != nil {
		return be
	}
	return nil
}

// Pending is the outcome of one command of a [Batch], available once the
// batch is sent.
type Pending[T any] struct {
	batch *Batch
	index int
	get   func(*reply) (T, error)
}

// Result returns the command's result, or its error: an [*Error] from
// Centrifugo for this command, the error that failed the whole batch, or
// [ErrBatchNotSent].
func (p *Pending[T]) Result() (T, error) {
	var zero T
	b := p.batch
	switch {
	case !b.sent:
		return zero, ErrBatchNotSent
	case b.err != nil:
		return zero, b.err
	}
	r := &b.replies[p.index]
	if r.Error != nil {
		return zero, r.Error
	}
	return p.get(r)
}

func add[T any](b *Batch, req request, get func(*reply) (T, error)) *Pending[T] {
	return addChecked(b, req, get, nil)
}

func addChecked[T any](b *Batch, req request, get func(*reply) (T, error), check func(*reply) error) *Pending[T] {
	if b.sent {
		panic("gocent: command added to a batch that was already sent")
	}
	var cmd command
	req.addTo(&cmd)
	b.commands = append(b.commands, cmd)
	b.methods = append(b.methods, req.APIMethod())
	b.checks = append(b.checks, check)
	b.invalid = append(b.invalid, validateRequest(req))
	return &Pending[T]{batch: b, index: len(b.commands) - 1, get: get}
}

// BatchError reports a batch in which one or more commands failed, up to all
// of them. Every command got its own reply: each one's outcome, success or
// failure, is available from its [Pending], and len(Failed) < Total tells
// whether any command succeeded.
//
// [errors.Is] and [errors.As] see the errors of the failed commands.
type BatchError struct {
	// Failed lists the failed commands in the order they were added.
	Failed []CommandError
	// Total is how many commands the batch had.
	Total int
}

// CommandError is the failure of one command of a batch.
type CommandError struct {
	// Index is the command's position in the batch, from zero.
	Index int
	// Method is the command's API method, such as "publish".
	Method string
	// Err is an [*Error], or a [*BroadcastError] for a broadcast which
	// failed in one or more of its channels.
	Err error
}

func (e *BatchError) Error() string {
	var b strings.Builder
	b.WriteString("gocent: batch: ")
	b.WriteString(strconv.Itoa(len(e.Failed)))
	b.WriteString(" of ")
	b.WriteString(strconv.Itoa(e.Total))
	b.WriteString(" commands failed")
	for i, f := range e.Failed {
		if i == maxErrorItems {
			b.WriteString("; and ")
			b.WriteString(strconv.Itoa(len(e.Failed) - i))
			b.WriteString(" more")
			break
		}
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		b.WriteString("#")
		b.WriteString(strconv.Itoa(f.Index))
		b.WriteString(" ")
		b.WriteString(f.Method)
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(f.Err.Error(), "gocent: "))
	}
	return b.String()
}

// Unwrap returns the error of every failed command.
func (e *BatchError) Unwrap() []error {
	errs := make([]error, len(e.Failed))
	for i, f := range e.Failed {
		errs[i] = f.Err
	}
	return errs
}

type batchRequest struct {
	Commands          []command `json:"commands"`
	Parallel          bool      `json:"parallel,omitzero"`
	GroupPublications bool      `json:"group_publications,omitzero"`
}

type batchResponse struct {
	Replies []reply `json:"replies"`
}

// sendBatch sends commands to the batch endpoint, and returns a reply for
// each, in order.
func (c *Client) sendBatch(ctx context.Context, commands []command, opts BatchOptions) ([]reply, error) {
	req := batchRequest{Commands: commands, Parallel: opts.Parallel, GroupPublications: opts.GroupPublications}
	var resp batchResponse
	if err := c.post(ctx, "batch", &req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Replies) != len(commands) {
		return nil, &DecodeError{Err: errMismatchedReplies(len(resp.Replies), len(commands), "commands")}
	}
	return resp.Replies, nil
}
