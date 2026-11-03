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

// HistoryAll returns the publications of a channel's history, oldest first,
// a page of req.Limit at a time: from req.Since when it is set, from the
// start of what the channel keeps otherwise. A page is read when the
// iteration reaches it, and the iteration ends after a page shorter than
// req.Limit, or with an error:
//
//	for pub, err := range client.HistoryAll(ctx, gocent.HistoryRequest{Channel: "news", Limit: 100}) {
//		if err != nil {
//			return err
//		}
//		handle(pub)
//	}
//
// req.Limit is the page size, so it must be 1 or more, and req.Reverse is
// refused: Since reads forward only. A publication made while iterating may or
// may not be seen. An epoch which changed since a page was read - the history
// was lost, with a Redis restart say - fails with [ErrUnrecoverablePosition]:
// the publications after the last one seen are gone, and reading again from
// the start is the only way on.
func (c *Client) HistoryAll(ctx context.Context, req HistoryRequest) iter.Seq2[Publication, error] {
	return func(yield func(Publication, error) bool) {
		switch {
		case req.Limit < 1:
			yield(Publication{}, invalidRequest("history", "HistoryAll needs a Limit of 1 or more: the page size"))
			return
		case req.Reverse:
			yield(Publication{}, invalidRequest("history", "HistoryAll reads forward only: Reverse is not supported"))
			return
		}
		for {
			res, err := c.History(ctx, req)
			if err != nil {
				yield(Publication{}, err)
				return
			}
			for _, pub := range res.Publications {
				if !yield(pub, nil) {
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
// size. An entry changed while iterating may be seen in either version, or
// not at all; for a consistent view, read the state with [Client.MapReadState]
// and the changes since its [MapReadStateResult.Position] with
// [Client.MapReadStream].
func (c *Client) MapReadStateAll(ctx context.Context, req MapReadStateRequest) iter.Seq2[MapEntry, error] {
	return byCursor(ctx, req, c.MapReadState,
		func(r MapReadStateResult) ([]MapEntry, string) { return r.Entries, r.Cursor },
		func(r *MapReadStateRequest, cursor string) { r.Cursor = cursor })
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
