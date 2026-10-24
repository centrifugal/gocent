package gocent

// Bool returns a [BoolValue] holding v, for the fields which take one, such as
// the channel options a subscription overrides:
//
//	gocent.SubscribeRequest{
//		Channel:  "chat:1",
//		User:     "42",
//		Override: gocent.SubscribeOptionOverride{Presence: gocent.Bool(true)},
//	}
//
// A nil *BoolValue leaves the channel's option as configured; Bool(false)
// turns it off for this subscription.
func Bool(v bool) *BoolValue { return &BoolValue{Value: v} }
