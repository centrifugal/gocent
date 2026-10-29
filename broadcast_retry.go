package gocent

import (
	"context"
	"crypto/rand"
	"errors"
	mathrand "math/rand/v2"
	"time"
)

// BroadcastRetryOptions controls how [Client.BroadcastWithRetry] retries.
// The zero value is ready to use: one retry, after a wait of about 100
// milliseconds.
//
// Options for a broadcast which may take a while - each attempt bounded on
// its own, a wait honouring a Retry-After, in seconds, the server sent:
//
//	backoff := gocent.ExponentialBackoff(200*time.Millisecond, 5*time.Second)
//	ctx, cancel := context.WithTimeout(ctx, 10*time.Second) // the whole call
//	defer cancel()
//	res, err := client.BroadcastWithRetry(ctx, req, gocent.BroadcastRetryOptions{
//		MaxAttempts:    4,
//		AttemptTimeout: 2 * time.Second, // each attempt
//		OnAttemptErr: func(ctx context.Context, a gocent.BroadcastAttempt) error {
//			wait := backoff(a.Attempt)
//			if httpErr, ok := errors.AsType[*gocent.HTTPError](a.Err); ok {
//				if secs, err := strconv.Atoi(httpErr.Header.Get("Retry-After")); err == nil {
//					wait = max(wait, time.Duration(secs)*time.Second)
//				}
//			}
//			return gocent.Wait(ctx, wait)
//		},
//	})
type BroadcastRetryOptions struct {
	// MaxAttempts is how many times the broadcast is sent at most, the first
	// time included. Zero means 2: one retry.
	MaxAttempts int
	// OnAttemptErr is called after each attempt which failed in a way a retry
	// may fix, except the last. It decides what happens before the next
	// attempt: wait - with [Wait], so that the wait ends with the context -
	// log, count, or stop. Nil means waiting
	// ExponentialBackoff(100*time.Millisecond, 5*time.Second)(a.Attempt).
	OnAttemptErr BroadcastAttemptErrFunc
	// AttemptTimeout bounds each attempt, while the call's context bounds
	// them all, waits between them included: an attempt which hangs times
	// out, and its channels are retried while the context has time left.
	// Zero means no bound of its own: the context alone bounds each attempt.
	// To bound every request of the client instead, set http.Client.Timeout
	// in Config.HTTPClient.
	AttemptTimeout time.Duration
}

// BroadcastAttemptErrFunc is called by [Client.BroadcastWithRetry] after an
// attempt failed in a way a retry may fix - see [Retryable] - except the last
// attempt, with the call's context. It is called in the caller's goroutine.
//
// It returns nil to go on with the next attempt, after waiting inside it as
// long as it likes - [Wait] waits until the context ends. Any error stops the
// retries: the call returns errors.Join of it and the broadcast's error, so
// that both errors.Is(err, stopErr) and a *BroadcastFailedChannelsError stay
// reachable - or the broadcast's error alone when it already holds the
// error, the context's say - and the result holds every channel's last
// outcome.
//
// A failure no retry can fix - a 401, an invalid request, only channels which
// failed for good - never reaches it: BroadcastWithRetry stops by itself.
type BroadcastAttemptErrFunc func(ctx context.Context, a BroadcastAttempt) error

// BroadcastAttempt describes a failed attempt of [Client.BroadcastWithRetry].
type BroadcastAttempt struct {
	// Attempt is the attempt which failed, from 1.
	Attempt int
	// Err is its error: a [*BroadcastFailedChannelsError] when Centrifugo
	// replied and some channels failed, otherwise the error of the request as a
	// whole.
	Err error
	// Retry are the channels the next attempt sends, with the errors this
	// attempt failed them with. A broadcast to many channels which all failed
	// lists every one: log a count or a few, not all of them.
	Retry []ChannelResult
	// Result is every channel's outcome so far, as the call would return it
	// if it stopped now. Its Channels slice is the call's own, updated by the
	// next attempts: read it only before returning, and copy what you keep -
	// to hand to another goroutine, say. Do not modify it.
	Result BroadcastResult
	// IdempotencyKey is the key every attempt is sent with.
	IdempotencyKey string
}

const defaultMaxAttempts = 2

// defaultOnAttemptErr is the OnAttemptErr of BroadcastRetryOptions which set
// none.
var defaultOnAttemptErr = func() BroadcastAttemptErrFunc {
	backoff := ExponentialBackoff(100*time.Millisecond, 5*time.Second)
	return func(ctx context.Context, a BroadcastAttempt) error {
		return Wait(ctx, backoff(a.Attempt))
	}
}()

// ExponentialBackoff returns the wait before retrying after a failed attempt,
// with attempt 1 being the first failure: base after it, twice as long after
// each next one, up to limit. Each wait is a random time between half of it
// and all of it, so that clients which failed together - when a broker was
// down - do not all retry at the same moment. A negative base counts as zero,
// a limit below base as base, and an attempt below 1 as 1. It is safe for
// concurrent use.
//
// It waits for nothing itself: pass its result to [Wait]. With
// [Client.BroadcastWithRetry], to do something of your own and keep the
// default wait:
//
//	backoff := gocent.ExponentialBackoff(100*time.Millisecond, 5*time.Second)
//	opts := gocent.BroadcastRetryOptions{
//		OnAttemptErr: func(ctx context.Context, a gocent.BroadcastAttempt) error {
//			log.Printf("attempt %d: %d channels to retry: %v", a.Attempt, len(a.Retry), a.Err)
//			return gocent.Wait(ctx, backoff(a.Attempt))
//		},
//	}
//
// And in a retry loop of your own:
//
//	for attempt := 1; ; attempt++ {
//		_, err := client.Publish(ctx, req)
//		if err == nil || !gocent.Retryable(err) || attempt == 3 {
//			return err
//		}
//		if err := gocent.Wait(ctx, backoff(attempt)); err != nil {
//			return err
//		}
//	}
func ExponentialBackoff(base, limit time.Duration) func(attempt int) time.Duration {
	base = max(base, 0)
	limit = max(limit, base)
	return func(attempt int) time.Duration {
		return jitter(backoffStep(base, limit, attempt))
	}
}

// backoffStep is the wait before the retry after attempt, before jitter:
// base, doubling, up to limit.
func backoffStep(base, limit time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < limit; i++ {
		if d > limit/2 {
			// Doubling would pass the limit - or overflow, for a limit
			// near the largest Duration.
			return limit
		}
		d *= 2
	}
	return min(d, limit)
}

// Wait waits for d, or until ctx ends, and then returns ctx's error. It is
// the wait to use between retries, inside a [BroadcastAttemptErrFunc] say:
// time.Sleep would keep the call - and Client.Close - waiting after ctx
// ended.
func Wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jitter returns a random wait between half of d and d.
func jitter(d time.Duration) time.Duration {
	half := d / 2
	return half + mathrand.N(d-half+1) //nolint:gosec // spreads retries out; nothing depends on it being unpredictable
}

// BroadcastWithRetry broadcasts as [Client.Broadcast] does, and retries the
// channels which failed for a temporary reason - see [Retryable] - up to
// opts.MaxAttempts, with opts.OnAttemptErr deciding what happens between
// attempts: by default, a short wait.
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
// ctx bounds the whole call, the waits between attempts included, and
// opts.AttemptTimeout each attempt. Once ctx ends no attempt follows.
//
// It returns as Broadcast does: a nil error when every channel got the
// publication, and a [*BroadcastFailedChannelsError] listing the channels which
// still failed after the last attempt. Any other error means no attempt got a
// reply. When OnAttemptErr stops it - the default one does when ctx ends while
// it waits - the error joins OnAttemptErr's with the broadcast's, unless the
// broadcast's already holds it, and the result holds what the attempts so far
// achieved.
//
// Retry a broadcast this way, rather than with a retry library which calls
// Broadcast again on any error: such a library sends the publication to
// every channel again, those which already got it included.
func (c *Client) BroadcastWithRetry(ctx context.Context, req BroadcastRequest, opts BroadcastRetryOptions) (BroadcastResult, error) {
	attempts, onAttemptErr := opts.MaxAttempts, opts.OnAttemptErr
	switch {
	case attempts < 0:
		err := invalidRequest("broadcast", "BroadcastRetryOptions.MaxAttempts is negative")
		return failedBroadcast(req.Channels, err), err
	case attempts == 0:
		attempts = defaultMaxAttempts
	}
	if opts.AttemptTimeout < 0 {
		err := invalidRequest("broadcast", "BroadcastRetryOptions.AttemptTimeout is negative")
		return failedBroadcast(req.Channels, err), err
	}
	if onAttemptErr == nil {
		onAttemptErr = defaultOnAttemptErr
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
		res, err := c.broadcastAttempt(ctx, attemptReq, opts.AttemptTimeout)
		final.Attempts += res.Attempts
		for i, pos := range pending {
			tried := final.Channels[pos].Attempts
			final.Channels[pos] = res.Channels[i]
			final.Channels[pos].Attempts += tried
		}
		lastErr = err
		if _, partial := errors.AsType[*BroadcastFailedChannelsError](err); err == nil || partial {
			replied = true
		}
		// An ended context leaves no time for another attempt, whatever
		// OnAttemptErr would do.
		if err == nil || !Retryable(err) || attempt == attempts || ctx.Err() != nil {
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
		retry := make([]ChannelResult, len(pending))
		for i, pos := range pending {
			retry[i] = final.Channels[pos]
		}
		final.Replied = replied
		stopErr := onAttemptErr(ctx, BroadcastAttempt{
			Attempt:        attempt,
			Err:            err,
			Retry:          retry,
			Result:         final,
			IdempotencyKey: req.IdempotencyKey,
		})
		if stopErr != nil {
			finalErr := finalBroadcastError(&final, replied, lastErr)
			if errors.Is(finalErr, stopErr) {
				// The context's error, say, which the attempt's error
				// already holds: naming it twice says nothing more.
				return final, finalErr
			}
			return final, errors.Join(stopErr, finalErr)
		}
	}
	err := finalBroadcastError(&final, replied, lastErr)
	return final, err
}

// broadcastAttempt sends one attempt of BroadcastWithRetry, bounded by
// timeout when it is set.
func (c *Client) broadcastAttempt(ctx context.Context, req BroadcastRequest, timeout time.Duration) (BroadcastResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return c.Broadcast(ctx, req)
}

// finalBroadcastError is the error of a broadcast sent in several attempts
// whose outcomes final holds: nil when every channel got the publication, a
// BroadcastFailedChannelsError listing those which did not when some attempt
// got a reply, and otherwise the error of the last attempt - no channel got a
// reply.
func finalBroadcastError(final *BroadcastResult, replied bool, lastErr error) error {
	final.Replied = replied
	if !replied {
		return lastErr
	}
	var be *BroadcastFailedChannelsError
	for _, ch := range final.Channels {
		if ch.Err == nil {
			continue
		}
		if be == nil {
			be = &BroadcastFailedChannelsError{Total: len(final.Channels)}
		}
		be.Failed = append(be.Failed, ch)
	}
	if be == nil {
		return nil
	}
	return be
}
