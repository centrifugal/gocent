package gocent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"sync"
)

// batcher implements [AutoBatch]. It counts requests in flight; below the cap
// a call is sent at once from the caller's goroutine, at the cap it queues.
// Whenever a request finishes and calls are queued, the finished request's
// slot passes straight to one batch carrying them - so the number of requests
// in flight never exceeds the cap, and nothing ever waits on a timer.
type batcher struct {
	client      *Client
	maxInFlight int
	maxBatch    int

	mu       sync.Mutex
	inFlight int
	queue    []*queued
}

// queued is a call waiting to be sent in a batch.
type queued struct {
	// command is the call's command, encoded by the caller before it
	// queued. The batch sends these bytes, so it never reads the caller's
	// request - which the caller may reuse once its call returns, even while
	// the batch is still on its way.
	command jsontext.Value
	// done is closed once reply or err is set. Until then the fields below
	// belong to the batcher; afterwards, to the caller.
	done  chan struct{}
	reply reply
	err   error
	// taken is set, under the batcher's lock, once the call is in a batch;
	// gone once its caller gave up waiting before that.
	taken bool
	gone  bool
}

// batched sends req as a call of its own when a request slot is free, and in
// the next batch otherwise.
func batched[W any](ctx context.Context, b *batcher, req request, get func(*reply) *W) (W, error) {
	var zero W
	var encoded jsontext.Value
	b.mu.Lock()
	for {
		b.dropGone()
		if b.inFlight < b.maxInFlight && len(b.queue) == 0 {
			b.inFlight++
			b.mu.Unlock()
			return sendAlone[W](ctx, b, req)
		}
		if encoded != nil {
			break
		}
		// The call will queue: encode its command first, outside the lock,
		// then look again - a slot may have freed meanwhile, and a call
		// must never queue while one is free, or nothing would send it.
		b.mu.Unlock()
		var err error
		if encoded, err = encodeCommand(req); err != nil {
			return zero, err
		}
		b.mu.Lock()
	}
	q := &queued{command: encoded, done: make(chan struct{})}
	b.queue = append(b.queue, q)
	b.mu.Unlock()

	select {
	case <-q.done:
	case <-ctx.Done():
		b.mu.Lock()
		taken := q.taken
		if !taken {
			q.gone = true
		}
		b.mu.Unlock()
		if taken {
			// Already sent: the batch completes without this caller, and
			// whether this command took effect is unknown to it.
			return zero, fmt.Errorf("gocent: %s: %w", req.APIMethod(), ctx.Err())
		}
		return zero, fmt.Errorf("gocent: %s: %w before it was sent", req.APIMethod(), ctx.Err())
	}
	switch {
	case q.err != nil:
		return zero, q.err
	case q.reply.Error != nil:
		return zero, q.reply.Error
	}
	return deref(get(&q.reply)), nil
}

// sendAlone sends req as a call of its own in a slot the caller took, and
// frees the slot afterwards - deferred, so that a panic in the request, in a
// user's BearerTokenFunc or transport, say, does not keep it.
func sendAlone[W any](ctx context.Context, b *batcher, req request) (W, error) {
	defer b.release() //nolint:contextcheck // a batch serves several callers, so it is not bound to this one's context
	return send(ctx, b.client, req, func(r *response[W]) (W, error) {
		if r.Error != nil {
			var zero W
			return zero, r.Error
		}
		return deref(r.Result), nil
	})
}

// encodeCommand encodes req as a batch command.
func encodeCommand(req request) (jsontext.Value, error) {
	var cmd command
	req.addTo(&cmd)
	encoded, err := json.Marshal(&cmd)
	if err != nil {
		return nil, encodingError(req.APIMethod(), err)
	}
	return encoded, nil
}

// release hands the slot of a finished request to the calls waiting, if any,
// and frees it otherwise.
func (b *batcher) release() {
	b.mu.Lock()
	b.dropGone()
	if len(b.queue) == 0 {
		b.inFlight--
		b.mu.Unlock()
		return
	}
	n := 0
	items := make([]*queued, 0, min(len(b.queue), b.maxBatch))
	for _, q := range b.queue {
		if len(items) == b.maxBatch {
			break
		}
		n++
		if q.gone {
			continue
		}
		q.taken = true
		items = append(items, q)
	}
	rest := copy(b.queue, b.queue[n:])
	clear(b.queue[rest:])
	b.queue = b.queue[:rest]
	b.mu.Unlock()
	// Sent from a goroutine of its own, so no caller's goroutine is held by
	// work done for others, and a caller giving up does not cancel it.
	go b.flush(items)
}

// dropGone removes calls whose callers gave up from the head of the queue, so
// that a queue holding only those does not stop a call from being sent at
// once. The caller holds the lock.
func (b *batcher) dropGone() {
	n := 0
	for n < len(b.queue) && b.queue[n].gone {
		n++
	}
	if n == 0 {
		return
	}
	rest := copy(b.queue, b.queue[n:])
	clear(b.queue[rest:])
	b.queue = b.queue[:rest]
}

func (b *batcher) flush(items []*queued) {
	defer b.release()
	if len(items) == 0 {
		return
	}
	commands := make([]jsontext.Value, len(items))
	for i, q := range items {
		commands[i] = q.command
	}
	// The batch serves several callers, so no one caller's context may end
	// it; the request timeout bounds it instead.
	ctx, cancel := context.WithTimeout(context.Background(), b.client.timeout)
	defer cancel()
	// Parallel: the commands of an automatic batch come from calls made
	// concurrently - a goroutine waits for one call before making the next,
	// so two calls of one goroutine are never in the same batch. Nothing
	// orders them against each other, and running them in parallel lets
	// Centrifugo pipeline their broker commands, as it does for separate
	// requests, instead of waiting for each in turn.
	replies, err := sendBatch(ctx, b.client, commands, BatchOptions{Parallel: true})
	for i, q := range items {
		if err != nil {
			q.err = err
		} else {
			q.reply = replies[i]
		}
		close(q.done)
	}
}
