//go:build integration

package gocent_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// TestIntegrationAutoBatchStress runs rounds of concurrent publications
// through AutoBatch against a real Centrifugo, with random caller deadlines
// and channels in an unknown namespace, then reads each channel's history to
// check what must hold whatever the interleaving:
//
//   - every call gets its own outcome: a success's offset and data are its
//     own publication's;
//   - no publication happens twice;
//   - a call which says it failed "before it was sent" never happened;
//   - a call with a generous deadline never times out: no call is stuck;
//   - OnRequest's counts agree with the publications which happened.
func TestIntegrationAutoBatchStress(t *testing.T) {
	rounds := 20
	if v, err := strconv.Atoi(os.Getenv("GOCENT_STRESS_ROUNDS")); err == nil {
		rounds = v
	}
	seed := rand.Uint64()
	if v, err := strconv.ParseUint(os.Getenv("GOCENT_STRESS_SEED"), 10, 64); err == nil {
		seed = v
	}
	t.Logf("seed %d (GOCENT_STRESS_SEED to repeat the choices)", seed)
	rng := rand.New(rand.NewPCG(seed, 0))
	// Reads history, so it must not be the client under test.
	reader := realClient(t, gocent.AutoBatch{})
	prefix := uniqueChannel(t)
	var seen struct{ Batches, Published, UnknownChannel, GaveUpQueued, GaveUpSent int }
	for round := range rounds {
		r := rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))
		calls := 50 + r.IntN(150)
		maxInFlight := 1 + r.IntN(4)
		timeouts := make([]time.Duration, calls)
		channels := make([]string, calls)
		bad := make([]bool, calls)
		for i := range calls {
			channels[i] = fmt.Sprintf("%s-%d-%d", prefix, round, i)
			if r.IntN(8) == 0 {
				bad[i] = true
				channels[i] = "unknown:" + channels[i]
			}
			switch r.IntN(3) {
			case 0: // none: RequestTimeout
			case 1:
				timeouts[i] = time.Duration(100+r.IntN(3000)) * time.Microsecond
			default:
				timeouts[i] = 5 * time.Second
			}
		}

		var (
			mu       sync.Mutex
			reported []gocent.RequestInfo
		)
		c, err := gocent.New(gocent.Config{
			APIEndpoint: testAddr(),
			APIKey:      testAPIKey(),
			AutoBatch:   gocent.AutoBatch{MaxInFlight: maxInFlight, MaxBatchSize: 2 + r.IntN(50)},
			OnRequest: func(_ context.Context, info gocent.RequestInfo) {
				mu.Lock()
				reported = append(reported, info)
				mu.Unlock()
			},
		})
		if err != nil {
			t.Fatal(err)
		}

		results := make([]gocent.PublishResult, calls)
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
				results[i], errs[i] = c.Publish(ctx, gocent.PublishRequest{
					Channel: channels[i],
					Data:    jsontext.Value(`{"i":` + strconv.Itoa(i) + `}`),
				})
			})
		}
		close(start)
		wg.Wait()
		// Batches whose callers all left may still be on their way: Close
		// waits for them, so that the history read below is final.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := c.Close(ctx); err != nil {
			t.Fatalf("round %d: Close: %v", round, err)
		}
		cancel()

		fail := func(format string, args ...any) {
			t.Helper()
			t.Errorf("round %d (%d calls, MaxInFlight %d): %s", round, calls, maxInFlight, fmt.Sprintf(format, args...))
		}
		published := 0
		for i := range calls {
			ch, err := channels[i], errs[i]
			var pubs []gocent.Publication
			if !bad[i] {
				h, herr := reader.History(t.Context(), gocent.HistoryRequest{Channel: ch, Limit: -1})
				if herr != nil {
					t.Fatalf("history of %s: %v", ch, herr)
				}
				pubs = h.Publications
			}
			published += len(pubs)
			if len(pubs) > 1 {
				fail("%s was published %d times", ch, len(pubs))
			}
			switch {
			case err == nil:
				seen.Published++
				want := `{"i":` + strconv.Itoa(i) + `}`
				if bad[i] || len(pubs) != 1 || pubs[0].Offset != results[i].Offset || string(pubs[0].Data) != want {
					fail("%s: succeeded with %+v, history %+v", ch, results[i], pubs)
				}
			case errors.Is(err, gocent.ErrUnknownChannel):
				seen.UnknownChannel++
				if !bad[i] {
					fail("%s: %v", ch, err)
				}
			case errors.Is(err, context.DeadlineExceeded):
				if timeouts[i] == 0 || timeouts[i] >= time.Second {
					fail("%s: %v with a timeout of %v: stuck", ch, err, timeouts[i])
				}
				if !strings.Contains(err.Error(), "before it was sent") {
					seen.GaveUpSent++
					break
				}
				seen.GaveUpQueued++
				if len(pubs) != 0 {
					fail("%s: %v, but it was published", ch, err)
				}
			default:
				fail("%s: unexpected error %v", ch, err)
			}
		}

		// Every command answered without an error is a publication; a
		// request which timed out on the client may have published too.
		answeredOK, unanswered := 0, 0
		for _, info := range reported {
			if info.AutoBatch != (info.Method == "batch") {
				fail("OnRequest reported %+v", info)
			}
			if info.AutoBatch {
				seen.Batches++
			}
			switch {
			case info.StatusCode == http.StatusOK && info.Err == nil:
				answeredOK += info.Commands - info.Failed
			case info.StatusCode == 0 && errors.Is(info.Err, context.DeadlineExceeded):
				unanswered += info.Commands
			default:
				fail("OnRequest reported %+v", info)
			}
		}
		if published < answeredOK || published > answeredOK+unanswered {
			fail("%d publications happened; OnRequest reported %d answered, %d unanswered", published, answeredOK, unanswered)
		}
		if t.Failed() {
			return
		}
	}
	t.Logf("%+v", seen)
}
