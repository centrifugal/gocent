package gocent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
)

// Broadcast publishes the same data into many channels in one request. It is
// the way to send one message to many channels: Centrifugo does the work of
// all the publications at once.
//
// Channels succeed or fail on their own, and the result always holds one
// entry per channel of req.Channels, in their order: the publication, or the
// channel's error. When the broadcast failed as a whole - the network, the
// credentials, Centrifugo refusing the request - every channel's error is that
// error.
//
// Like every call, Broadcast returns an error unless it did everything asked:
// err is nil only when the data was published into every channel. When
// Centrifugo carried out the broadcast and one or more channels failed - up
// to all of them - err is a [*BroadcastFailedChannelsError]. Any other error
// means the broadcast failed as a whole: no channel got a reply. After a
// timeout or a network error Centrifugo may still have published it.
//
// Most callers need no more than err != nil. A broadcast retried with the same
// IdempotencyKey is not published again into the channels which already have
// it, so retrying the whole broadcast is safe - with the memory, Redis and
// PostgreSQL brokers, which keep keys; the NATS broker ignores them.
//
// To retry a broadcast, use [Client.BroadcastWithRetry]: it sends again only
// the channels which may still succeed, with one IdempotencyKey - made for the
// call when the request has none - and merges the attempts' results. A
// generic retry helper - one which calls Broadcast again on any error -
// sends the publication to every channel again after some failed, those
// which got it included; without an IdempotencyKey they get it twice.
//
// To go on despite some failed channels:
//
//	res, err := client.Broadcast(ctx, req)
//	if !res.Replied {
//		return err // no channel got a reply
//	}
//	for _, ch := range res.Channels {
//		if ch.Err != nil {
//			log.Printf("not published into %s: %v", ch.Channel, ch.Err)
//		}
//	}
func (c *Client) Broadcast(ctx context.Context, req BroadcastRequest) (BroadcastResult, error) {
	w, err := invoke(ctx, c, &req, func(r *reply) *broadcastWire { return r.Broadcast })
	if err != nil {
		res := failedBroadcast(req.Channels, err)
		res.finish(req, err)
		return res, err
	}
	res, err := w.result(req.Channels)
	res.finish(req, err)
	return res, err
}

// Broadcast adds a broadcast command to the batch. Its outcome, read with
// [Pending.Result] once the batch is sent, is what [Client.Broadcast] would
// have returned: every channel's outcome, and a [*BroadcastFailedChannelsError]
// when any channel failed.
func (b *Batch) Broadcast(req BroadcastRequest) *Pending[BroadcastResult] {
	channels := req.Channels
	// The outcome is built once, on first use - by Send's check or by
	// Pending.Result - and shared: a broadcast into many channels is costly to
	// build, and Send's BatchFailedCommandsError then holds the very
	// *BroadcastFailedChannelsError which Pending.Result returns.
	var (
		once sync.Once
		res  BroadcastResult
		err  error
	)
	get := func(r *reply) (BroadcastResult, error) {
		once.Do(func() {
			res, err = deref(r.Broadcast).result(channels)
			res.finish(req, err)
		})
		return res, err
	}
	// A broadcast which failed in any channel failed as a command too, so
	// Send counts it in its BatchFailedCommandsError.
	check := func(r *reply) error { _, err := get(r); return err }
	p := addChecked(b, &req, get, check)
	p.fail = func(err error) BroadcastResult {
		res := failedBroadcast(channels, err)
		res.finish(req, err)
		return res
	}
	return p
}

// BroadcastResult is the outcome of a broadcast: one entry per channel, in the
// order the request listed them.
type BroadcastResult struct {
	Channels []ChannelResult

	// IdempotencyKey is the key the broadcast was sent with: the request's,
	// or the one [Client.BroadcastWithRetry] made when the request had none.
	// A later retry of the same broadcast - from a job queue, say - which
	// sends it with this key does not publish it again into the channels
	// which got it.
	IdempotencyKey string

	// Attempts is how many times the broadcast was tried: 1 from
	// [Client.Broadcast], up to BroadcastRetryOptions.MaxAttempts from
	// [Client.BroadcastWithRetry], and 0 when it was refused before sending,
	// as an invalid request.
	Attempts int

	// Replied is true when Centrifugo carried the broadcast out and replied
	// for every channel, the failed ones included - the error is nil or a
	// [*BroadcastFailedChannelsError]. It is false when the broadcast failed as
	// a whole: a network error, a timeout, an HTTP error status such as 401, an
	// invalid request; no channel got a reply then, and every channel carries
	// that error. From [Client.BroadcastWithRetry], it is true when any attempt
	// got a reply. gocent sets it to agree with the error; a test double may
	// set it to stand for either case.
	//
	//	res, err := client.Broadcast(ctx, req)
	//	if !res.Replied {
	//		return err // no channel got a reply
	//	}
	//	for _, ch := range res.Failed() { ... } // the channels which failed
	Replied bool
}

// Failed returns the channels which did not get the publication, with their
// errors, in the order of the request; nil when every channel got it. For a
// broadcast which failed as a whole, that is every channel.
func (res BroadcastResult) Failed() []ChannelResult {
	var failed []ChannelResult
	for _, ch := range res.Channels {
		if ch.Err != nil {
			failed = append(failed, ch)
		}
	}
	return failed
}

// finish completes a broadcast's result with what the request and its error
// say: the key it was sent with, and whether it was tried at all - a request
// refused before sending was not.
func (res *BroadcastResult) finish(req BroadcastRequest, err error) {
	res.IdempotencyKey = req.IdempotencyKey
	_, failedChannels := errors.AsType[*BroadcastFailedChannelsError](err)
	res.Replied = err == nil || failedChannels
	if errors.Is(err, ErrInvalidRequest) || errors.Is(err, ErrBatchNotSent) {
		return
	}
	res.Attempts = 1
	for i := range res.Channels {
		res.Channels[i].Attempts = 1
	}
}

// ChannelResult is the outcome of a broadcast in one channel.
type ChannelResult struct {
	// Channel is the channel, as the request named it.
	Channel string
	// Result is the publication's position in the channel's history. It is
	// zero when the channel keeps no history, and when Err is set.
	Result PublishResult
	// Err is nil when the publication succeeded. Otherwise it is the
	// channel's [*Error], or the error of the whole broadcast when it failed
	// as a whole.
	Err error
	// Attempts is how many times the channel was sent: more than 1 when
	// [Client.BroadcastWithRetry] retried it.
	Attempts int
}

// BroadcastFailedChannelsError reports a broadcast which Centrifugo carried out
// and which failed in one or more of its channels, up to all of them. The
// result returned with it holds every channel's outcome.
//
// It does not unwrap to the channels' errors: errors.Is(err,
// ErrUnknownChannel) is not true because one channel of many failed that way.
// Look at each channel's error in Failed instead:
//
//	for _, f := range be.Failed {
//		if errors.Is(f.Err, gocent.ErrUnknownChannel) { ... }
//	}
type BroadcastFailedChannelsError struct {
	// Failed lists the channels which failed, with their errors, in the order
	// of the request.
	Failed []ChannelResult
	// Total is how many channels the broadcast had.
	Total int
}

func (e *BroadcastFailedChannelsError) Error() string {
	return failuresMessage("broadcast", "channels", len(e.Failed), e.Total, func(b *strings.Builder, i int) (string, error) {
		b.WriteString(e.Failed[i].Channel)
		return "", e.Failed[i].Err
	})
}

// RetryableChannels returns the channels whose failure is retryable - see
// [Retryable] - in the order of the request: the channels a retry of the
// broadcast should go to. Channels which failed for good, such as an unknown
// channel, are left out; they are in Failed with their errors. It returns nil
// when no failure is retryable.
//
//	if be, ok := errors.AsType[*gocent.BroadcastFailedChannelsError](err); ok {
//		req.Channels = be.RetryableChannels() // the same IdempotencyKey
//	}
func (e *BroadcastFailedChannelsError) RetryableChannels() []string {
	var channels []string
	for _, f := range e.Failed {
		if Retryable(f.Err) {
			channels = append(channels, f.Channel)
		}
	}
	return channels
}

// failedBroadcast is the result of a broadcast which failed as a whole: every
// channel failed with err.
func failedBroadcast(channels []string, err error) BroadcastResult {
	res := BroadcastResult{Channels: make([]ChannelResult, len(channels))}
	for i, ch := range channels {
		res.Channels[i] = ChannelResult{Channel: ch, Err: err}
	}
	return res
}

// broadcastWire is a broadcast result as Centrifugo sends it: a response per
// channel, positionally matching the request's channels.
type broadcastWire struct {
	Responses []channelResponse `json:"responses"`
}

// channelResponse is one channel's reply. Its result is a value, not the
// pointer a method's envelope uses: a broadcast decodes one per channel, and a
// pointer would cost an allocation each.
type channelResponse struct {
	Error  *Error        `json:"error,omitzero"`
	Result PublishResult `json:"result,omitzero"`
}

// result pairs each response with its channel. The channels are the
// request's: the reply does not repeat them.
func (w broadcastWire) result(channels []string) (BroadcastResult, error) {
	if len(w.Responses) != len(channels) {
		err := &DecodeError{Err: errMismatchedReplies(len(w.Responses), len(channels), "channels")}
		return failedBroadcast(channels, err), err
	}
	res := BroadcastResult{Channels: make([]ChannelResult, len(channels))}
	failed := 0
	for i := range w.Responses {
		r := &w.Responses[i]
		cr := &res.Channels[i]
		cr.Channel = channels[i]
		if r.Error != nil {
			cr.Err = r.Error
			failed++
			continue
		}
		cr.Result = r.Result
	}
	if failed == 0 {
		return res, nil
	}
	be := &BroadcastFailedChannelsError{Failed: make([]ChannelResult, 0, failed), Total: len(channels)}
	for _, cr := range res.Channels {
		if cr.Err != nil {
			be.Failed = append(be.Failed, cr)
		}
	}
	return res, be
}

type mismatchedReplies struct {
	got, want int
	of        string
}

func (e mismatchedReplies) Error() string {
	return "got " + strconv.Itoa(e.got) + " replies for " + strconv.Itoa(e.want) + " " + e.of
}

func errMismatchedReplies(got, want int, of string) error {
	return mismatchedReplies{got: got, want: want, of: of}
}
