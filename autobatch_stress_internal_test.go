package gocent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stressTransport plays Centrifugo for TestAutoBatchStress: it answers
// publications, alone or in batches, after a random delay, failing some
// requests as a whole with 503 and publications into the channels in bad
// with "unknown channel". It counts how often each channel reached it.
type stressTransport struct {
	bad map[string]bool

	mu   sync.Mutex
	rng  *rand.Rand
	sent map[string]int
	// batches counts batch requests.
	batches int
	// requests holds what each request carried and how it was answered, as
	// OnRequest must report it.
	requests []stressRequest

	inFlight, maxInFlight atomic.Int64
}

func (s *stressTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	n := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for m := s.maxInFlight.Load(); n > m && !s.maxInFlight.CompareAndSwap(m, n); m = s.maxInFlight.Load() {
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	type publish struct {
		Channel string `json:"channel"`
	}
	var channels []string
	batch := strings.HasSuffix(r.URL.Path, "/batch")
	if batch {
		var req struct {
			Commands []struct {
				Publish *publish `json:"publish"`
			} `json:"commands"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		for _, cmd := range req.Commands {
			channels = append(channels, cmd.Publish.Channel)
		}
	} else {
		var req publish
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		channels = []string{req.Channel}
	}

	// The request reached the server: whatever happens next, its
	// publications may happen.
	s.mu.Lock()
	if batch {
		s.batches++
	}
	for _, ch := range channels {
		s.sent[ch]++
	}
	delay := time.Duration(s.rng.IntN(2000)) * time.Microsecond
	unavailable := s.rng.IntN(10) == 0
	s.mu.Unlock()

	method := "publish"
	if batch {
		method = "batch"
	}
	answered := stressRequest{method: method, commands: len(channels)}
	defer func() {
		s.mu.Lock()
		s.requests = append(s.requests, answered)
		s.mu.Unlock()
	}()

	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if unavailable {
		answered.statusCode = http.StatusServiceUnavailable
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: http.Header{}}, nil
	}
	answered.statusCode = http.StatusOK
	replies := make([]string, len(channels))
	for i, ch := range channels {
		if s.bad[ch] {
			answered.failed++
			replies[i] = `{"error":{"code":102,"message":"unknown channel"}}`
			continue
		}
		key := "result"
		if batch {
			key = "publish"
		}
		replies[i] = fmt.Sprintf(`{%q:{"offset":1,"epoch":"e-%s"}}`, key, ch)
	}
	reply := replies[0]
	if batch {
		reply = `{"replies":[` + strings.Join(replies, ",") + `]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(reply))),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}, nil
}

// stressRequest is what OnRequest must report for a request.
type stressRequest struct {
	method     string
	commands   int
	failed     int
	statusCode int
}

// TestAutoBatchStress runs rounds of concurrent publications through
// AutoBatch, with random caller deadlines, server delays, failed channels and
// requests failing as a whole, and checks what must hold whatever the
// interleaving:
//
//   - every call gets its own outcome, never another call's;
//   - no publication reaches the server twice;
//   - a call which says it failed "before it was sent" never reached it - its
//     caller may retry it without a key;
//   - no more than MaxInFlight requests are in flight at once;
//   - a call with a generous deadline never times out: no call is stuck;
//   - afterwards nothing is queued or in flight, and Close returns;
//   - OnRequest reports every request once, as it was answered.
//
// Scheduling decides the interleaving, so a failure may not repeat with the
// same seed; the seed still fixes every other choice.
func TestAutoBatchStress(t *testing.T) {
	rounds := 200
	if testing.Short() {
		rounds = 20
	}
	if v, err := strconv.Atoi(os.Getenv("GOCENT_STRESS_ROUNDS")); err == nil {
		rounds = v
	}
	seed := rand.Uint64()
	if v, err := strconv.ParseUint(os.Getenv("GOCENT_STRESS_SEED"), 10, 64); err == nil {
		seed = v
	}
	t.Logf("seed %d (GOCENT_STRESS_SEED to repeat the choices)", seed)
	rng := rand.New(rand.NewPCG(seed, 0))
	var seen stressOutcomes
	for round := range rounds {
		stressRound(t, round, rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64())), &seen)
		if t.Failed() {
			return
		}
	}
	t.Logf("%+v", seen)
	// The scenarios must keep happening, or the checks above check nothing:
	// a change in timing could make, say, every caller give up before its
	// call is sent.
	if rounds >= 100 && (seen.Batches == 0 || seen.Published == 0 || seen.UnknownChannel == 0 ||
		seen.Unavailable == 0 || seen.GaveUpQueued == 0 || seen.GaveUpSent == 0) {
		t.Errorf("a scenario never happened: %+v", seen)
	}
}

// stressOutcomes counts what the rounds exercised.
type stressOutcomes struct {
	Batches, Published, UnknownChannel, Unavailable int
	// GaveUpQueued counts callers which gave up before their call was sent;
	// GaveUpSent, after.
	GaveUpQueued, GaveUpSent int
}

func stressRound(t *testing.T, round int, rng *rand.Rand, seen *stressOutcomes) {
	calls := 10 + rng.IntN(50)
	maxInFlight := 1 + rng.IntN(4)
	tr := &stressTransport{bad: map[string]bool{}, rng: rand.New(rand.NewPCG(rng.Uint64(), 0)), sent: map[string]int{}}
	timeouts := make([]time.Duration, calls)
	for i := range calls {
		ch := "c" + strconv.Itoa(i)
		if rng.IntN(8) == 0 {
			tr.bad[ch] = true
		}
		switch rng.IntN(3) {
		case 0: // none: RequestTimeout
		case 1:
			timeouts[i] = time.Duration(200+rng.IntN(4000)) * time.Microsecond
		default:
			timeouts[i] = time.Second
		}
	}
	var (
		reportedMu sync.Mutex
		reported   []RequestInfo
	)
	c, err := New(Config{
		APIEndpoint: "http://centrifugo.invalid/api",
		APIKey:      "k",
		HTTPClient:  &http.Client{Transport: tr},
		OnRequest: func(_ context.Context, info RequestInfo) {
			reportedMu.Lock()
			reported = append(reported, info)
			reportedMu.Unlock()
		},
		AutoBatch: AutoBatch{MaxInFlight: maxInFlight, MaxBatchSize: 2 + rng.IntN(20)},
	})
	if err != nil {
		t.Fatal(err)
	}

	results := make([]PublishResult, calls)
	errs := make([]error, calls)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range calls {
		wg.Go(func() {
			<-start
			ctx := context.Background()
			if timeouts[i] > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeouts[i])
				defer cancel()
			}
			results[i], errs[i] = c.Publish(ctx, PublishRequest{Channel: "c" + strconv.Itoa(i), Data: jsontext.Value(`{}`)})
		})
	}
	close(start)
	wg.Wait()

	// Batches whose callers all left may still be on their way: Close waits
	// for them, so that the counts below are final.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatalf("round %d: Close: %v", round, err)
	}

	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("round %d (%d calls, MaxInFlight %d): %s", round, calls, maxInFlight, fmt.Sprintf(format, args...))
	}
	for i := range calls {
		ch := "c" + strconv.Itoa(i)
		sent, err := tr.sent[ch], errs[i]
		if sent > 1 {
			fail("%s reached the server %d times", ch, sent)
		}
		httpErr, isHTTP := errors.AsType[*HTTPError](err)
		switch {
		case err == nil:
			seen.Published++
			if tr.bad[ch] || sent != 1 || results[i].Epoch != "e-"+ch {
				fail("%s: succeeded with %+v, sent %d times, bad %v", ch, results[i], sent, tr.bad[ch])
			}
		case errors.Is(err, ErrUnknownChannel):
			seen.UnknownChannel++
			if !tr.bad[ch] || sent != 1 {
				fail("%s: %v, sent %d times, bad %v", ch, err, sent, tr.bad[ch])
			}
		case isHTTP:
			seen.Unavailable++
			if httpErr.StatusCode != http.StatusServiceUnavailable || sent != 1 {
				fail("%s: %v, sent %d times", ch, err, sent)
			}
		case errors.Is(err, context.DeadlineExceeded):
			// The server answers within milliseconds, so a call with a
			// deadline of a second or more never runs out of time - unless
			// it was stuck, waiting for a batch which never came.
			if timeouts[i] == 0 || timeouts[i] >= time.Second {
				fail("%s: %v with a timeout of %v: stuck", ch, err, timeouts[i])
			}
			if !strings.Contains(err.Error(), "before it was sent") {
				seen.GaveUpSent++
				break
			}
			seen.GaveUpQueued++
			if sent != 0 {
				fail("%s: %v, but it reached the server", ch, err)
			}
		default:
			fail("%s: unexpected error %v", ch, err)
		}
	}
	seen.Batches += tr.batches
	if got := tr.maxInFlight.Load(); got > int64(maxInFlight) {
		fail("%d requests in flight at once", got)
	}
	checkReported(fail, reported, tr.requests)
	b := c.batcher
	b.mu.Lock()
	inFlight, queued := b.inFlight, len(b.queue)
	b.mu.Unlock()
	if inFlight != 0 || queued != 0 {
		fail("afterwards %d requests in flight, %d calls queued", inFlight, queued)
	}
}

// checkReported checks that OnRequest reported each request the server got
// once, as the server answered it - matched as multisets, since requests run
// concurrently.
func checkReported(fail func(string, ...any), reported []RequestInfo, answered []stressRequest) {
	got := make([]stressRequest, 0, len(reported))
	for _, info := range reported {
		if info.AutoBatch != (info.Method == "batch") || info.Duration < 0 {
			fail("OnRequest reported %+v", info)
		}
		httpErr, isHTTP := errors.AsType[*HTTPError](info.Err)
		switch {
		case info.StatusCode == http.StatusOK && info.Err != nil,
			info.StatusCode == http.StatusServiceUnavailable && (!isHTTP || httpErr.StatusCode != info.StatusCode),
			info.StatusCode == 0 && !errors.Is(info.Err, context.DeadlineExceeded):
			fail("OnRequest reported status %d with error %v", info.StatusCode, info.Err)
		}
		got = append(got, stressRequest{method: info.Method, commands: info.Commands, failed: info.Failed, statusCode: info.StatusCode})
	}
	key := func(r stressRequest) string {
		return fmt.Sprintf("%s/%d/%d/%d", r.method, r.commands, r.failed, r.statusCode)
	}
	count := map[string]int{}
	for _, r := range answered {
		count[key(r)]++
	}
	for _, r := range got {
		count[key(r)]--
	}
	for k, n := range count {
		if n != 0 {
			fail("method/commands/failed/status %s: %d more answered than reported (%d reported, %d answered)", k, n, len(got), len(answered))
		}
	}
}
