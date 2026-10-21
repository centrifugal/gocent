// Package sharedpoll signs what Centrifugo's shared poll subscriptions track,
// and serves the signatures to client SDKs.
//
// A client tracking keys of a shared poll channel presents a signature its
// backend issued over them. [Sign] makes one; [Handlers.Signature] serves the
// endpoint a subscription's getSignature callback calls, around a function of
// the application's deciding which keys the user may track:
//
//	h := sharedpoll.Handlers{OnError: logError}
//	mux.Handle("/centrifugo/shared_poll_signature", h.Signature(
//		func(r *http.Request, channel string, keys []string) ([]string, string, error) {
//			user, ok := sessionUser(r)
//			if !ok {
//				return nil, "", sharedpoll.ErrUnauthorized
//			}
//			allowed := readable(r.Context(), user, channel, keys)
//			return allowed, sharedpoll.Sign(secret, user, channel, allowed, time.Now().Add(time.Hour)), nil
//		}))
//
// The two are independent: sign anywhere, or serve signatures made elsewhere.
//
// Shared poll subscriptions are experimental in Centrifugo, and this package
// follows their changes. See https://centrifugal.dev/docs/server/shared_poll.
package sharedpoll
