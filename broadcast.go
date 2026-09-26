package gocent

import (
	"context"
	"strconv"
	"strings"
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
// to all of them - err is a [*BroadcastError]. Any other error means the
// broadcast failed as a whole: no channel got a reply. After a timeout or a
// network error Centrifugo may still have published it.
//
// Most callers need no more than err != nil. A broadcast retried with the same
// IdempotencyKey is not published again into the channels which already have
// it, so retrying the whole broadcast is safe. To go on despite some failed
// channels:
//
//	res, err := client.Broadcast(ctx, req)
//	var be *gocent.BroadcastError
//	if err != nil && !errors.As(err, &be) {
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
		return failedBroadcast(req.Channels, err), err
	}
	return w.result(req.Channels)
}

// Broadcast adds a broadcast command to the batch. Its outcome, read with
// [Pending.Result] once the batch is sent, is what [Client.Broadcast] would
// have returned: every channel's outcome, and a [*BroadcastError] when any
// channel failed.
func (b *Batch) Broadcast(req BroadcastRequest) *Pending[BroadcastResult] {
	channels := req.Channels
	get := func(r *reply) (BroadcastResult, error) { return deref(r.Broadcast).result(channels) }
	// A broadcast which failed in any channel failed as a command too, so
	// Send counts it in its BatchError.
	check := func(r *reply) error { _, err := get(r); return err }
	p := addChecked(b, &req, get, check)
	p.fail = func(err error) BroadcastResult { return failedBroadcast(channels, err) }
	return p
}

// BroadcastResult is the outcome of a broadcast: one entry per channel, in the
// order the request listed them.
type BroadcastResult struct {
	Channels []ChannelResult
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
}

// BroadcastError reports a broadcast which Centrifugo carried out and which
// failed in one or more of its channels, up to all of them. The result
// returned with it holds every channel's outcome.
//
// [errors.Is] and [errors.As] see the errors of the failed channels, so
// errors.Is(err, ErrUnknownChannel) is true when any channel failed that way.
type BroadcastError struct {
	// Failed lists the channels which failed, with their errors, in the order
	// of the request.
	Failed []ChannelResult
	// Total is how many channels the broadcast had.
	Total int
}

func (e *BroadcastError) Error() string {
	var b strings.Builder
	b.WriteString("gocent: broadcast: ")
	b.WriteString(strconv.Itoa(len(e.Failed)))
	b.WriteString(" of ")
	b.WriteString(strconv.Itoa(e.Total))
	b.WriteString(" channels failed")
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
		b.WriteString(f.Channel)
		b.WriteString(": ")
		b.WriteString(strings.TrimPrefix(f.Err.Error(), "gocent: "))
	}
	return b.String()
}

// Unwrap returns the error of every failed channel.
func (e *BroadcastError) Unwrap() []error {
	errs := make([]error, len(e.Failed))
	for i, f := range e.Failed {
		errs[i] = f.Err
	}
	return errs
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
	be := &BroadcastError{Failed: make([]ChannelResult, 0, failed), Total: len(channels)}
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
