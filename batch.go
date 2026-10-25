package gocent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Batch collects commands and sends them to Centrifugo in one request. Create
// one with [Client.NewBatch], add commands with its methods - one per API
// method, such as [Batch.Publish] - then call [Batch.Send]:
//
//	b := client.NewBatch(gocent.BatchOptions{})
//	pub := b.Publish(gocent.PublishRequest{Channel: "news", Data: data})
//	stats := b.PresenceStats(gocent.PresenceStatsRequest{Channel: "news"})
//	if _, err := b.Send(ctx); err != nil {
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
	//
	// Without it, the commands run one by one, in the order they were added.
	// Centrifugo PRO sends a batch's publications to its broker together,
	// though: each channel's publications take effect in order, but
	// publications into different channels may not, unless the namespace sets
	// publication_grouping_disabled.
	Parallel bool
}

// ErrBatchNotSent is returned by [Pending.Result] before its batch is sent.
var ErrBatchNotSent = errors.New("gocent: batch not sent yet")

// ErrBatchSent is returned by [Batch.Send] for a batch already sent.
var ErrBatchSent = errors.New("gocent: batch already sent")

// NewBatch returns an empty [Batch] sent by c and run as opts say.
// BatchOptions{} runs the commands one by one, in the order they were added
// (see [BatchOptions.Parallel] for publications with Centrifugo PRO).
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
//   - A [*BatchFailedCommandsError] means one or more commands failed, up to
//     all of them. Every command got its own reply: each [Pending] holds its
//     outcome, success or failure, and the error lists the failed ones.
//   - Any other error means the batch failed as a whole: no command got a
//     reply, and every Pending returns that same error.
//
// The result says which: BatchResult.Replied is true when Centrifugo replied
// to every command, and [BatchResult.Failed] lists the commands which failed. Code which only needs the error writes
// _, err := b.Send(ctx).
//
// A command the client can tell is invalid - see [ErrInvalidRequest] - fails
// the whole batch before anything is sent, and every Pending returns that
// error.
//
// Sending an empty batch does nothing and returns nil. A batch is sent once,
// whatever Send returns - fix a batch by building it again. Sending it again
// returns [ErrBatchSent].
func (b *Batch) Send(ctx context.Context) (BatchResult, error) {
	if b.sent {
		return BatchResult{}, ErrBatchSent
	}
	b.sent = true
	if err := b.client.calls.enter(); err != nil {
		b.err = err
		return b.failedAsWhole(err), err
	}
	defer b.client.calls.leave()
	if len(b.commands) == 0 {
		return BatchResult{Replied: true}, nil
	}
	// A command the client can tell is invalid would fail the whole batch
	// when encoded - or reach Centrifugo only to be refused - so nothing is
	// sent, and the error names the command.
	for i, err := range b.invalid {
		if err != nil {
			b.err = &commandInvalidError{index: i, err: err}
			return b.failedAsWhole(b.err), b.err
		}
	}
	replies, err := sendBatch(ctx, b.client, b.commands, b.opts, false)
	if err != nil {
		if errors.Is(err, ErrInvalidRequest) {
			// A command which cannot be encoded - a payload which is not
			// valid JSON - failed the batch's encoding: name it.
			err = b.unencodable(err)
		}
		b.err = err
		return b.failedAsWhole(err), err
	}
	b.replies = replies

	var be *BatchFailedCommandsError
	for i := range replies {
		var err error
		switch {
		case replies[i].Error != nil:
			err = replies[i].Error.inBatch(b.methods[i])
		case b.checks[i] != nil:
			err = b.checks[i](&replies[i])
		}
		if err == nil {
			continue
		}
		if be == nil {
			be = &BatchFailedCommandsError{Total: len(replies)}
		}
		be.Failed = append(be.Failed, CommandError{Index: i, Method: b.methods[i], Err: err})
	}
	if be != nil {
		return BatchResult{Replied: true, failed: be.Failed}, be
	}
	return BatchResult{Replied: true}, nil
}

// BatchResult is the outcome of a sent batch as a whole. Each command's own
// result is in the [Pending] its method returned.
type BatchResult struct {
	// Replied is true when Centrifugo carried the batch out and replied to
	// every command, the failed ones included - Send's error is nil or a
	// [*BatchFailedCommandsError]. It is false when the batch failed as a
	// whole: a network error, a timeout, an HTTP error status such as 401, or a
	// command the client refused before sending; no command got a reply then.
	// It is true for an empty batch.
	Replied bool

	failed []CommandError
}

// Failed returns the commands which failed, with their errors, in the order
// they were added; nil when every command succeeded. When the batch failed as
// a whole, that is every command, each with the batch's error - as each
// Pending returns it.
func (r BatchResult) Failed() []CommandError { return r.failed }

// failedAsWhole is the result of a batch which failed as a whole with err:
// every command failed with it.
func (b *Batch) failedAsWhole(err error) BatchResult {
	failed := make([]CommandError, len(b.methods))
	for i, method := range b.methods {
		failed[i] = CommandError{Index: i, Method: method, Err: err}
	}
	return BatchResult{failed: failed}
}

// unencodable finds the command which failed the batch's encoding, and
// returns its error naming it - or err, if no single command fails alone.
func (b *Batch) unencodable(err error) error {
	for i := range b.commands {
		if _, cmdErr := json.Marshal(&b.commands[i]); cmdErr != nil {
			return &commandInvalidError{index: i, err: encodingError(b.methods[i], cmdErr)}
		}
	}
	return err
}

// commandInvalidError is the error of a batch not sent because one of its
// commands is invalid: "gocent: batch command #1: invalid publish request:
// ...". It wraps the command's error, so [ErrInvalidRequest] matches it.
type commandInvalidError struct {
	index int
	err   error
}

func (e *commandInvalidError) Error() string {
	return "gocent: batch command #" + strconv.Itoa(e.index) + ": " + strings.TrimPrefix(e.err.Error(), "gocent: ")
}

func (e *commandInvalidError) Unwrap() error { return e.err }

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

// BatchFailedCommandsError reports a batch in which one or more commands
// failed, up to all of them. Every command got its own reply: each one's
// outcome, success or failure, is available from its [Pending].
//
// It does not unwrap to the commands' errors: errors.Is(err, ErrUnknownChannel)
// is not true because one command of many failed that way. Look at each
// command's error in Failed, or at its [Pending].
type BatchFailedCommandsError struct {
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
	// [*BroadcastFailedChannelsError] for a broadcast which failed in one or
	// more of its channels.
	Err error
}

func (e *BatchFailedCommandsError) Error() string {
	return failuresMessage("batch", "commands", len(e.Failed), e.Total, func(b *strings.Builder, i int) (string, error) {
		f := e.Failed[i]
		b.WriteString("#")
		b.WriteString(strconv.Itoa(f.Index))
		b.WriteString(" ")
		b.WriteString(f.Method)
		return f.Method, f.Err
	})
}

// failuresMessage is the message of an error listing the failed parts of a
// request - "gocent: batch: 2 of 3 commands failed: ...". It names at most
// maxErrorItems of them; label writes the name of the i-th and returns its
// error, and the prefix its message may repeat - "broadcast" for a broadcast
// in a batch, whose message starts with "broadcast: ".
func failuresMessage(op, parts string, failed, total int, label func(b *strings.Builder, i int) (string, error)) string {
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
		repeated, err := label(&b, i)
		msg := strings.TrimPrefix(err.Error(), "gocent: ")
		if repeated != "" {
			msg = strings.TrimPrefix(msg, repeated+": ")
		}
		b.WriteString(": ")
		b.WriteString(msg)
	}
	return b.String()
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
func sendBatch[C any](ctx context.Context, c *Client, commands []C, opts BatchOptions, auto bool) ([]reply, error) {
	req := batchRequest[C]{Commands: commands, Parallel: opts.Parallel}
	var resp batchResponse
	info := RequestInfo{Method: "batch", Commands: len(commands), AutoBatch: auto}
	start := time.Now()
	err := c.post(ctx, "batch", &req, &resp, &info.StatusCode)
	if err == nil && len(resp.Replies) != len(commands) {
		err = &DecodeError{Err: errMismatchedReplies(len(resp.Replies), len(commands), "commands")}
	}
	if err == nil {
		for i := range resp.Replies {
			if resp.Replies[i].Error != nil {
				info.Failed++
			}
		}
	}
	c.reportRequest(ctx, info, start, err)
	if err != nil {
		return nil, err
	}
	return resp.Replies, nil
}
