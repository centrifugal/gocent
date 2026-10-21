package gocent

import (
	"context"
	"crypto/rand"
	"errors"
	mathrand "math/rand/v2"
	"time"
)

// RetryOptions controls how [Client.BroadcastWithRetry] retries.
type RetryOptions struct {
	// Attempts is how many times the broadcast is sent at most, the first
	// time included. Zero means 3.
	Attempts int
	// Backoff is the wait before the second attempt. It doubles before each
	// next one, up to MaxBackoff. Each wait is a random time between half of
	// it and all of it, so that clients which failed together - when a broker
	// was down - do not all retry at the same moment. Zero means 100
	// milliseconds.
	Backoff time.Duration
	// MaxBackoff caps the wait between two attempts. Zero means 5 seconds,
	// or Backoff if that is longer.
	MaxBackoff time.Duration
	// OnRetry, when set, is called before each retry, with the context of the
	// call: to log or count retries with what the context carries - a request
	// ID, a trace span. It is called in the caller's goroutine, and the retry
	// waits for it.
	//
	// It only learns of the retry. To stop retrying from it, the caller
	// creates the call's context with a cancel function the hook can reach -
	// a closure over context.WithCancelCause, say - and cancels it: the call
	// then stops before the next attempt.
	OnRetry func(ctx context.Context, e RetryEvent)
}

// RetryEvent describes a retry of [Client.BroadcastWithRetry], for
// RetryOptions.OnRetry.
type RetryEvent struct {
	// Attempt is the attempt which just failed, from 1.
	Attempt int
	// Failed are the channels the next attempt sends, with the errors this
	// attempt failed them with. A broadcast to many channels which all failed
	// lists every one: log a count or a few, not all of them.
	Failed []ChannelResult
	// Wait is how long until the next attempt.
	Wait time.Duration
	// IdempotencyKey is the key every attempt is sent with.
	IdempotencyKey string
}

const (
	defaultRetryAttempts   = 3
	defaultRetryBackoff    = 100 * time.Millisecond
	defaultRetryMaxBackoff = 5 * time.Second
)

// jitter returns a random wait between half of d and d.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + mathrand.N(d-half+1) //nolint:gosec // spreads retries out; nothing depends on it being unpredictable
}

// BroadcastWithRetry broadcasts as [Client.Broadcast] does, and retries the
// channels which failed for a temporary reason - see [Retryable] - backing off
// between attempts:
//
//   - Only the channels which may still succeed are sent again. A channel
//     which failed for good, an unknown channel say, is not, and when only
//     such channels are left, it stops.
//   - Every attempt carries the same IdempotencyKey, so that a channel which
//     got the publication in an earlier attempt - even one reported as failed,
//     after a timeout - does not get it again. When req has none, a random one
//     is made for this call. Brokers which keep keys honour it: memory, Redis
//     and PostgreSQL; with the NATS broker a retry may publish again.
//   - The result holds every channel's last outcome: the publication's
//     position from whichever attempt succeeded, or the error of its last
//     attempt. Its IdempotencyKey is the key the attempts used: retrying the
//     broadcast later with it, after this call gave up, publishes into only
//     the channels which did not get it yet.
//
// It returns as Broadcast does: a nil error when every channel got the
// publication, and a [*BroadcastError] listing the channels which still failed
// after the last attempt. Any other error means no attempt got a reply. When
// ctx ends, it stops: while backing off, it returns what the attempts so far
// have achieved.
//
// Retry a broadcast this way, rather than with a retry library which calls
// Broadcast again on any error: such a library sends the publication to
// every channel again, those which already got it included.
func (c *Client) BroadcastWithRetry(ctx context.Context, req BroadcastRequest, opts RetryOptions) (BroadcastResult, error) {
	attempts, backoff, maxBackoff := opts.Attempts, opts.Backoff, opts.MaxBackoff
	switch {
	case attempts < 0 || backoff < 0 || maxBackoff < 0:
		err := invalidRequest("broadcast", "RetryOptions has a negative Attempts, Backoff or MaxBackoff")
		return failedBroadcast(req.Channels, err), err
	case attempts == 0:
		attempts = defaultRetryAttempts
	}
	if backoff == 0 {
		backoff = defaultRetryBackoff
	}
	if maxBackoff == 0 {
		maxBackoff = max(defaultRetryMaxBackoff, backoff)
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = "gocent-" + rand.Text()
	}

	channels := req.Channels
	final := BroadcastResult{Channels: make([]ChannelResult, len(channels)), IdempotencyKey: req.IdempotencyKey}
	// pending holds the positions in channels of what the next attempt sends:
	// positions, not names, so that a channel listed twice keeps both entries.
	pending := make([]int, len(channels))
	for i := range pending {
		pending[i] = i
	}
	replied := false
	var lastErr error
	for attempt := 1; ; attempt++ {
		attemptReq := req
		attemptReq.Channels = make([]string, len(pending))
		for i, pos := range pending {
			attemptReq.Channels[i] = channels[pos]
		}
		res, err := c.Broadcast(ctx, attemptReq)
		final.Attempts += res.Attempts
		for i, pos := range pending {
			tried := final.Channels[pos].Attempts
			final.Channels[pos] = res.Channels[i]
			final.Channels[pos].Attempts += tried
		}
		lastErr = err
		if _, partial := errors.AsType[*BroadcastError](err); err == nil || partial {
			replied = true
		}
		if err == nil || !Retryable(err) || attempt == attempts {
			break
		}
		// Next, the channels which may still succeed: of a broadcast which
		// failed in part, those whose error is retryable; of one which failed
		// as a whole, all of them.
		next := pending[:0:0]
		for i, pos := range pending {
			if chErr := res.Channels[i].Err; chErr != nil && Retryable(chErr) {
				next = append(next, pos)
			}
		}
		pending = next
		wait := jitter(min(backoff, maxBackoff))
		if opts.OnRetry != nil {
			failed := make([]ChannelResult, len(pending))
			for i, pos := range pending {
				failed[i] = final.Channels[pos]
			}
			opts.OnRetry(ctx, RetryEvent{Attempt: attempt, Failed: failed, Wait: wait, IdempotencyKey: req.IdempotencyKey})
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return final, finalBroadcastError(final, replied, lastErr)
		}
		backoff = min(backoff*2, maxBackoff)
	}
	return final, finalBroadcastError(final, replied, lastErr)
}

// finalBroadcastError is the error of a broadcast sent in several attempts
// whose outcomes final holds: nil when every channel got the publication, a
// BroadcastError listing those which did not when some attempt got a reply,
// and otherwise the error of the last attempt - no channel got a reply.
func finalBroadcastError(final BroadcastResult, replied bool, lastErr error) error {
	if !replied {
		return lastErr
	}
	var be *BroadcastError
	for _, ch := range final.Channels {
		if ch.Err == nil {
			continue
		}
		if be == nil {
			be = &BroadcastError{Total: len(final.Channels)}
		}
		be.Failed = append(be.Failed, ch)
	}
	if be == nil {
		return nil
	}
	return be
}
