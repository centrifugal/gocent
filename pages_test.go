package gocent_test

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/centrifugal/gocent/v4"
)

// pagedCentrifugo answers history and device_list over a stream of n items,
// recording what each request asked for.
type pagedCentrifugo struct {
	n        int
	failAt   int // the request, counting from 1, which fails; 0 for none
	requests []map[string]any
}

func (p *pagedCentrifugo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	p.requests = append(p.requests, req)
	if len(p.requests) == p.failAt {
		_, _ = w.Write([]byte(`{"error":{"code":100,"message":"internal server error","temporary":true}}`))
		return
	}
	limit, _ := req["limit"].(float64)
	var out any
	switch r.URL.Path {
	case "/api/history":
		from := 0
		if since, ok := req["since"].(map[string]any); ok {
			off, _ := since["offset"].(float64)
			from = int(off)
		}
		var pubs []map[string]any
		for i := from + 1; i <= p.n && len(pubs) < int(limit); i++ {
			pubs = append(pubs, map[string]any{"offset": i, "data": i})
		}
		out = map[string]any{"publications": pubs, "epoch": "e", "offset": p.n}
	case "/api/map_read_state":
		from, _ := strconv.Atoi(stringOr(req["cursor"]))
		var entries []map[string]any
		for i := from; i < p.n && len(entries) < int(limit); i++ {
			entries = append(entries, map[string]any{"key": "k" + strconv.Itoa(i)})
		}
		res := map[string]any{"entries": entries, "offset": 9, "epoch": "e"}
		if from+int(limit) < p.n {
			res["cursor"] = strconv.Itoa(from + int(limit))
		}
		out = res
	case "/api/device_list":
		from, _ := strconv.Atoi(stringOr(req["cursor"]))
		size := int(limit)
		var items []map[string]any
		for i := from; i < p.n && len(items) < size; i++ {
			items = append(items, map[string]any{"id": "d" + strconv.Itoa(i)})
		}
		res := map[string]any{"items": items}
		if from+size < p.n {
			res["next_cursor"] = strconv.Itoa(from + size)
		}
		out = res
	default:
		http.NotFound(w, r)
		return
	}
	b, _ := json.Marshal(map[string]any{"result": out})
	_, _ = w.Write(b)
}

func stringOr(v any) string {
	s, _ := v.(string)
	return s
}

func pagedClient(t *testing.T, p *pagedCentrifugo) *gocent.Client {
	t.Helper()
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	c, err := gocent.New(gocent.Config{APIEndpoint: srv.URL + "/api"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHistoryAll(t *testing.T) {
	for _, n := range []int{0, 1, 4, 5, 9} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			p := &pagedCentrifugo{n: n}
			c := pagedClient(t, p)
			var got []uint64
			for pub, err := range c.HistoryAll(t.Context(), gocent.HistoryRequest{Channel: "news", Limit: 2}) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, pub.Offset)
			}
			var want []uint64
			for i := 1; i <= n; i++ {
				want = append(want, uint64(i))
			}
			if !slices.Equal(got, want) {
				t.Errorf("offsets %v, want %v", got, want)
			}
			// A page per two, and one more which comes back short - empty when
			// n is even.
			if len(p.requests) != n/2+1 {
				t.Errorf("%d requests, want %d", len(p.requests), n/2+1)
			}
			// Each page asks from the last offset of the one before, with the
			// stream's epoch.
			for i, req := range p.requests[1:] {
				since, _ := req["since"].(map[string]any)
				if since["offset"] != float64(2*(i+1)) || since["epoch"] != "e" {
					t.Errorf("request %d since %v", i+2, since)
				}
			}
		})
	}
}

func TestHistoryAllRefuses(t *testing.T) {
	p := &pagedCentrifugo{n: 3}
	c := pagedClient(t, p)
	for name, req := range map[string]gocent.HistoryRequest{
		"no limit":    {Channel: "news"},
		"all at once": {Channel: "news", Limit: -1},
		"reverse":     {Channel: "news", Limit: 2, Reverse: true},
	} {
		n := 0
		for _, err := range c.HistoryAll(t.Context(), req) {
			n++
			if !errors.Is(err, gocent.ErrInvalidRequest) {
				t.Errorf("%s: %v", name, err)
			}
		}
		if n != 1 {
			t.Errorf("%s: %d iterations, want the one error", name, n)
		}
	}
	if len(p.requests) != 0 {
		t.Errorf("%d requests sent", len(p.requests))
	}
}

func TestDeviceListAll(t *testing.T) {
	p := &pagedCentrifugo{n: 5}
	c := pagedClient(t, p)
	var got []string
	for d, err := range c.DeviceListAll(t.Context(), gocent.DeviceListRequest{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d.ID)
	}
	if want := []string{"d0", "d1", "d2", "d3", "d4"}; !slices.Equal(got, want) {
		t.Errorf("devices %v, want %v", got, want)
	}
	if len(p.requests) != 3 {
		t.Errorf("%d requests, want 3", len(p.requests))
	}
}

// Breaking out of the loop reads no further page.
func TestPagesStopWithTheLoop(t *testing.T) {
	p := &pagedCentrifugo{n: 10}
	c := pagedClient(t, p)
	for d, err := range c.DeviceListAll(t.Context(), gocent.DeviceListRequest{Limit: 2}) {
		if err != nil {
			t.Fatal(err)
		}
		if d.ID == "d2" {
			break
		}
	}
	if len(p.requests) != 2 {
		t.Errorf("%d requests, want 2: the page holding d2 is the last read", len(p.requests))
	}
}

// An error ends the iteration, after the items of the pages before it.
func TestPagesEndWithAnError(t *testing.T) {
	p := &pagedCentrifugo{n: 10, failAt: 2}
	c := pagedClient(t, p)
	var got []string
	var errs []error
	for d, err := range c.DeviceListAll(t.Context(), gocent.DeviceListRequest{Limit: 2}) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		got = append(got, d.ID)
	}
	if !slices.Equal(got, []string{"d0", "d1"}) || len(errs) != 1 || !errors.Is(errs[0], gocent.ErrInternal) {
		t.Errorf("devices %v, errors %v", got, errs)
	}
}

func TestPosition(t *testing.T) {
	want := gocent.StreamPosition{Offset: 7, Epoch: "e"}
	for name, got := range map[string]gocent.StreamPosition{
		"publish":         gocent.PublishResult{Offset: 7, Epoch: "e"}.Position(),
		"history":         gocent.HistoryResult{Offset: 7, Epoch: "e"}.Position(),
		"map_read_state":  gocent.MapReadStateResult{Offset: 7, Epoch: "e"}.Position(),
		"map_read_stream": gocent.MapReadStreamResult{Offset: 7, Epoch: "e"}.Position(),
		"map_publish":     gocent.MapPublishResult{Offset: 7, Epoch: "e"}.Position(),
		"map_remove":      gocent.MapRemoveResult{Offset: 7, Epoch: "e"}.Position(),
	} {
		if got != want {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// Every page of a map state after the first is read at the first one's
// revision, so that a reset between pages fails instead of mixing states;
// a revision the caller set is kept.
func TestMapReadStateAllRevision(t *testing.T) {
	for name, tc := range map[string]struct {
		req          gocent.MapReadStateRequest
		first, later any // revision_offset of the first and later requests
	}{
		"from the first page": {gocent.MapReadStateRequest{Channel: "m", Limit: 2}, nil, float64(9)},
		"the caller's":        {gocent.MapReadStateRequest{Channel: "m", Limit: 2, RevisionOffset: 4, RevisionEpoch: "x"}, float64(4), float64(4)},
	} {
		t.Run(name, func(t *testing.T) {
			p := &pagedCentrifugo{n: 5}
			c := pagedClient(t, p)
			var keys []string
			for e, err := range c.MapReadStateAll(t.Context(), tc.req) {
				if err != nil {
					t.Fatal(err)
				}
				keys = append(keys, e.Key)
			}
			if len(keys) != 5 || len(p.requests) != 3 {
				t.Fatalf("keys %v in %d requests", keys, len(p.requests))
			}
			if got := p.requests[0]["revision_offset"]; got != tc.first {
				t.Errorf("first request revision_offset %v, want %v", got, tc.first)
			}
			for i, r := range p.requests[1:] {
				if r["revision_offset"] != tc.later || r["revision_epoch"] == nil {
					t.Errorf("request %d revision %v %v", i+2, r["revision_offset"], r["revision_epoch"])
				}
			}
		})
	}
}
