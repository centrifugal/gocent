package gocent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Batch collects commands and sends them to Centrifugo in one request. Create
// one with [Client.NewBatch], add commands with its methods - one per API
// method, such as [Batch.Publish] - then call [Batch.Send]:
//
//	b := client.NewBatch(gocent.BatchOptions{})
//	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
//	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
//	if err := b.Send(ctx); err != nil {
//		return err // not every command succeeded
//	}
//	res, err := pub.Result() // this command's own outcome
//
// Each method returns a [Pending] holding that command's outcome once the
// batch is sent. A Batch is sent once, and is not safe for concurrent use.
//
// A command keeps its request as given until the batch is sent: the request
// struct is copied, but its slices and maps - Channels, Data, Tags - are
// shared with the caller. Do not modify them until Send returns.
type Batch struct {
	client   *Client
	opts     BatchOptions
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

// BatchOptions controls how Centrifugo runs a batch. They are set when the
// batch is created, since they change what its commands mean together: in
// what order they take effect.
type BatchOptions struct {
	// Parallel lets Centrifugo run the commands concurrently. It is faster
	// for many independent commands, but the commands no longer take effect
	// in the order they were added.
	Parallel bool
}

// ErrBatchNotSent is returned by [Pending.Result] before its batch is sent.
var ErrBatchNotSent = errors.New("gocent: batch not sent yet")

// ErrBatchSent is returned by [Batch.Send] for a batch already sent.
var ErrBatchSent = errors.New("gocent: batch already sent")

// NewBatch returns an empty [Batch] sent by c and run as opts say.
// BatchOptions{} runs the commands one by one, in the order they were added.
func (c *Client) NewBatch(opts BatchOptions) *Batch {
	return &Batch{client: c, opts: opts}
}

// Len returns the number of commands in the batch.
func (b *Batch) Len() int { return len(b.commands) }

// Send sends the batch and waits for Centrifugo's reply to every command.
//
// Like every call, Send returns an error unless every command succeeded:
//
//   - nil means every command succeeded.
//   - A [*BatchError] means one or more commands failed, up to all of them.
//     Every command got its own reply: each [Pending] holds its outcome,
//     success or failure, and the error lists the failed ones.
//   - Any other error means the batch failed as a whole: no command got a
//     reply, and every Pending returns that same error.
//
// A command the client can tell is invalid - see [ErrInvalidRequest] - fails
// the whole batch before anything is sent, and every Pending returns that
// error.
//
// Sending an empty batch does nothing and returns nil. A batch is sent once,
// whatever Send returns - fix a batch by building it again. Sending it again
// returns [ErrBatchSent].
func (b *Batch) Send(ctx context.Context) error {
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
	replies, err := sendBatch(ctx, b.client, b.commands, b.opts)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			// A command which cannot be encoded - a payload which is not
			// valid JSON - failed the batch's encoding: name it.
			err = b.unencodable(err)
		}
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

// unencodable finds the command which failed the batch's encoding, and
// returns its error naming it - or err, if no single command fails alone.
func (b *Batch) unencodable(err error) error {
	for i := range b.commands {
		if _, cmdErr := json.Marshal(&b.commands[i]); cmdErr != nil {
			return fmt.Errorf("batch command #%d: %w", i, encodingError(b.methods[i], cmdErr))
		}
	}
	return err
}

// Pending is the outcome of one command of a [Batch], available once the
// batch is sent.
type Pending[T any] struct {
	batch *Batch
	index int
	get   func(*reply) (T, error)
	// fail, when set, gives the result returned with an error for the
	// command as a whole: a broadcast has an outcome for every channel even
	// then.
	fail func(error) T
}

// Result returns the command's result, or its error: an [*Error] from
// Centrifugo for this command, the error that failed the whole batch, or
// [ErrBatchNotSent]. A broadcast's result holds every channel's outcome
// whatever the error, as [Client.Broadcast] does.
func (p *Pending[T]) Result() (T, error) {
	b := p.batch
	switch {
	case !b.sent:
		return p.failed(ErrBatchNotSent)
	case b.err != nil:
		return p.failed(b.err)
	}
	r := &b.replies[p.index]
	if r.Error != nil {
		return p.failed(r.Error)
	}
	return p.get(r)
}

func (p *Pending[T]) failed(err error) (T, error) {
	if p.fail == nil {
		var zero T
		return zero, err
	}
	return p.fail(err), err
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
// failure, is available from its [Pending].
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
	// Err is the error the command's [Pending] returns: an [*Error], or a
	// [*BroadcastError] for a broadcast which failed in one or more of its
	// channels.
	Err error
}

func (e *BatchError) Error() string {
	return failuresMessage("batch", "commands", len(e.Failed), e.Total, func(b *strings.Builder, i int) error {
		f := e.Failed[i]
		b.WriteString("#")
		b.WriteString(strconv.Itoa(f.Index))
		b.WriteString(" ")
		b.WriteString(f.Method)
		return f.Err
	})
}

// Unwrap returns the error of every failed command.
func (e *BatchError) Unwrap() []error {
	return failureErrors(e.Failed, func(f CommandError) error { return f.Err })
}

// failuresMessage is the message of an error listing the failed parts of a
// request - "gocent: batch: 2 of 3 commands failed: ...". It names at most
// maxErrorItems of them; label writes the name of the i-th and returns its
// error.
func failuresMessage(op, parts string, failed, total int, label func(b *strings.Builder, i int) error) string {
	var b strings.Builder
	b.WriteString("gocent: ")
	b.WriteString(op)
	b.WriteString(": ")
	b.WriteString(strconv.Itoa(failed))
	b.WriteString(" of ")
	b.WriteString(strconv.Itoa(total))
	b.WriteString(" ")
	b.WriteString(parts)
	b.WriteString(" failed")
	for i := range failed {
		if i == maxErrorItems {
			b.WriteString("; and ")
			b.WriteString(strconv.Itoa(failed - i))
			b.WriteString(" more")
			break
		}
		if i == 0 {
			b.WriteString(": ")
		} else {
			b.WriteString("; ")
		}
		err := label(&b, i)
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(err.Error(), "gocent: "))
	}
	return b.String()
}

// failureErrors returns the error of every failed part.
func failureErrors[T any](failed []T, errOf func(T) error) []error {
	errs := make([]error, len(failed))
	for i, f := range failed {
		errs[i] = errOf(f)
	}
	return errs
}

// batchRequest is a batch as Centrifugo takes it. C is a command: a command
// struct for a [Batch], or one already encoded for an automatic batch.
type batchRequest[C any] struct {
	Commands []C  `json:"commands"`
	Parallel bool `json:"parallel,omitzero"`
}

type batchResponse struct {
	Replies []reply `json:"replies"`
}

// sendBatch sends commands to the batch endpoint, and returns a reply for
// each, in order.
func sendBatch[C any](ctx context.Context, c *Client, commands []C, opts BatchOptions) ([]reply, error) {
	req := batchRequest[C]{Commands: commands, Parallel: opts.Parallel}
	var resp batchResponse
	if err := c.post(ctx, "batch", &req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Replies) != len(commands) {
		return nil, &DecodeError{Err: errMismatchedReplies(len(resp.Replies), len(commands), "commands")}
	}
	return resp.Replies, nil
}
