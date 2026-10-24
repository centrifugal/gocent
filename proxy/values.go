package proxy

import "strconv"

// Bool returns a [BoolValue] holding v, for the channel options a
// subscription overrides:
//
//	proxy.SubscribeResult{
//		Override: proxy.SubscribeOptionOverride{Presence: proxy.Bool(true)},
//	}
//
// A nil *BoolValue leaves the channel's option as configured; Bool(false)
// turns it off for this subscription.
func Bool(v bool) *BoolValue { return &BoolValue{Value: v} }

// Error returns the error's message and code, so that an *Error can be
// returned as an error from the functions the handlers call.
func (e *Error) Error() string {
	return "proxy: " + e.Message + " (code " + strconv.FormatUint(uint64(e.Code), 10) + ")"
}

// Error returns the disconnect's reason and code, so that a *Disconnect can be
// returned as an error from the functions the handlers call.
func (d *Disconnect) Error() string {
	return "proxy: disconnect: " + d.Reason + " (code " + strconv.FormatUint(uint64(d.Code), 10) + ")"
}
