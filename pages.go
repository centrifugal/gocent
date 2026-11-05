package gocent

import (
	"context"
	"iter"
)

// Position returns where the publication went in the channel's stream: the
// Since of a [HistoryRequest] reading what came after it.
func (r PublishResult) Position() StreamPosition {
	return StreamPosition(r)
}

// Position returns the channel's stream position when the history was read.
func (r HistoryResult) Position() StreamPosition {
	return StreamPosition{Offset: r.Offset, Epoch: r.Epoch}
}

// Position returns the map channel's stream position when its state was
// read: with MapReadStreamRequest's SinceOffset and SinceEpoch, it reads the
// changes since.
func (r MapReadStateResult) Position() StreamPosition {
	return StreamPosition{Offset: r.Offset, Epoch: r.Epoch}
}

// Position returns the map channel's stream position when its stream was
// read.
func (r MapReadStreamResult) Position() StreamPosition {
	return StreamPosition{Offset: r.Offset, Epoch: r.Epoch}
}

// Position returns where the map publication went in the channel's stream.
func (r MapPublishResult) Position() StreamPosition {
	return StreamPosition{Offset: r.Offset, Epoch: r.Epoch}
}

// Position returns where the map removal went in the channel's stream.
func (r MapRemoveResult) Position() StreamPosition {
	return StreamPosition{Offset: r.Offset, Epoch: r.Epoch}
}

// HistoryItem is a publication read by [Client.HistoryAll], with the epoch of
// the stream it was read from: its Position is where to resume reading, or
// the RecoverSince of a subscription, without fields copied by hand.
type HistoryItem struct {
	Publication
	// Epoch is the epoch of the channel's stream the publication is in.
	Epoch string
}

// Position returns where the publication is in the channel's stream.
func (i HistoryItem) Position() StreamPosition {
	return StreamPosition{Offset: i.Offset, Epoch: i.Epoch}
}

// MapStateItem is an entry read by [Client.MapReadStateAll], with the
// position of the state it was read at: following the state with
// [Client.MapReadStream] from State gets every change made since.
type MapStateItem struct {
	MapEntry
	// State is the position of the map's state the entry was read at: the
	// first page's, every later page being read at it.
	State StreamPosition
}

// HistoryAll returns the publications of a channel's history, oldest first,
// a page of req.Limit at a time: from req.Since when it is set, from the
// start of what the channel keeps otherwise. A page is read when the
// iteration reaches it, and the iteration ends after a page shorter than
// req.Limit, or with an error:
//
//	for item, err := range client.HistoryAll(ctx, gocent.HistoryRequest{Channel: "news", Limit: 100}) {
//		if err != nil {
//			return err
//		}
//		handle(item.Data)
//		last = item.Position() // to resume from later
//	}
//
// req.Limit is the page size, so it must be 1 or more, and req.Reverse is
// refused: Since reads forward only. A publication made while iterating may or
// may not be seen. An epoch which changed since a page was read - the history
// was lost, with a Redis restart say - fails with [ErrUnrecoverablePosition]:
// the publications after the last one seen are gone, and reading again from
// the start is the only way on.
func (c *Client) HistoryAll(ctx context.Context, req HistoryRequest) iter.Seq2[HistoryItem, error] {
	return func(yield func(HistoryItem, error) bool) {
		switch {
		case req.Limit < 1:
			yield(HistoryItem{}, invalidRequest("history", "HistoryAll needs a Limit of 1 or more: the page size"))
			return
		case req.Reverse:
			yield(HistoryItem{}, invalidRequest("history", "HistoryAll reads forward only: Reverse is not supported"))
			return
		}
		for {
			res, err := c.History(ctx, req)
			if err != nil {
				yield(HistoryItem{}, err)
				return
			}
			for _, pub := range res.Publications {
				if !yield(HistoryItem{Publication: pub, Epoch: res.Epoch}, nil) {
					return
				}
			}
			n := len(res.Publications)
			if n < int(req.Limit) {
				return
			}
			req.Since = StreamPosition{Offset: res.Publications[n-1].Offset, Epoch: res.Epoch}
		}
	}
}

// DeviceListAll returns every device req selects, a page at a time, each
// page read when the iteration reaches it: req.Limit is the page size, the
// server's default when unset. A device registered or removed while
// iterating may or may not be seen. Centrifugo PRO only.
func (c *Client) DeviceListAll(ctx context.Context, req DeviceListRequest) iter.Seq2[Device, error] {
	return byCursor(ctx, req, c.DeviceList,
		func(r DeviceListResult) ([]Device, string) { return r.Items, r.NextCursor },
		func(r *DeviceListRequest, cursor string) { r.Cursor = cursor })
}

// DeviceTopicListAll returns every device topic req selects, a page at a
// time, as [Client.DeviceListAll] does. Centrifugo PRO only.
func (c *Client) DeviceTopicListAll(ctx context.Context, req DeviceTopicListRequest) iter.Seq2[DeviceTopic, error] {
	return byCursor(ctx, req, c.DeviceTopicList,
		func(r DeviceTopicListResult) ([]DeviceTopic, string) { return r.Items, r.NextCursor },
		func(r *DeviceTopicListRequest, cursor string) { r.Cursor = cursor })
}

// UserTopicListAll returns every user topic req selects, a page at a time,
// as [Client.DeviceListAll] does. Centrifugo PRO only.
func (c *Client) UserTopicListAll(ctx context.Context, req UserTopicListRequest) iter.Seq2[UserTopic, error] {
	return byCursor(ctx, req, c.UserTopicList,
		func(r UserTopicListResult) ([]UserTopic, string) { return r.Items, r.NextCursor },
		func(r *UserTopicListRequest, cursor string) { r.Cursor = cursor })
}

// MapReadStateAll returns every entry of a map channel's state, a page at a
// time, each page read when the iteration reaches it: req.Limit is the page
// size. Every page after the first is read at the first one's revision - its
// [MapReadStateResult.Position], unless req sets RevisionOffset or
// RevisionEpoch - so that a state reset between pages fails with
// [ErrUnrecoverablePosition] instead of mixing two states: read again from
// the start then. Entries changed while iterating may be seen in either
// version; for a consistent view, read the changes since with
// [Client.MapReadStream], from the State every [MapStateItem] carries.
func (c *Client) MapReadStateAll(ctx context.Context, req MapReadStateRequest) iter.Seq2[MapStateItem, error] {
	pinned := req.RevisionOffset > 0 || req.RevisionEpoch != ""
	entries := byCursor(ctx, req, c.MapReadState,
		func(r MapReadStateResult) ([]MapEntry, string) {
			if !pinned {
				req.RevisionOffset, req.RevisionEpoch = r.Offset, r.Epoch
				pinned = true
			}
			return r.Entries, r.Cursor
		},
		func(r *MapReadStateRequest, cursor string) {
			r.Cursor = cursor
			r.RevisionOffset, r.RevisionEpoch = req.RevisionOffset, req.RevisionEpoch
		})
	return func(yield func(MapStateItem, error) bool) {
		for e, err := range entries {
			state := StreamPosition{Offset: req.RevisionOffset, Epoch: req.RevisionEpoch}
			if !yield(MapStateItem{MapEntry: e, State: state}, err) {
				return
			}
		}
	}
}

// byCursor iterates the items of a method paged by a cursor: it sends req,
// then req with the cursor of each reply, until a reply has none.
func byCursor[Req, Res, Item any](
	ctx context.Context,
	req Req,
	call func(context.Context, Req) (Res, error),
	page func(Res) ([]Item, string),
	setCursor func(*Req, string),
) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		for {
			res, err := call(ctx, req)
			if err != nil {
				var zero Item
				yield(zero, err)
				return
			}
			items, next := page(res)
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if next == "" {
				return
			}
			setCursor(&req, next)
		}
	}
}
