package token

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/centrifugal/gocent/v4"
)

// ConnectionClaims are the claims of a connection token. See
// https://centrifugal.dev/docs/server/authentication.
type ConnectionClaims struct {
	// User is the ID of the user the connection belongs to: the "sub" claim.
	// Empty means an anonymous connection.
	User string
	// ExpiresAt is until when the token is accepted: the "exp" claim. The
	// connection expires then too, unless ConnectionExpiresAt or
	// ConnectionNoExpiry say otherwise: Centrifugo asks the client for a new
	// token, and closes the connection without one. Required, unless NoExpiry
	// is set.
	ExpiresAt time.Time
	// NoExpiry leaves the "exp" claim out: the token is accepted forever, and
	// works forever once leaked - for a trusted service's own connection, say.
	// It must be set explicitly, instead of ExpiresAt.
	NoExpiry bool
	// ConnectionExpiresAt, when set, is when the connection expires instead of
	// at ExpiresAt: the "expire_at" claim, for a token accepted briefly opening
	// a longer connection. Most applications leave it unset. With NoExpiry
	// the connection must still refresh at ConnectionExpiresAt, but the token
	// it was opened with stays accepted.
	ConnectionExpiresAt time.Time
	// ConnectionNoExpiry makes the connection never expire - the "expire_at"
	// claim zero - while ExpiresAt still bounds when the token is accepted.
	// Centrifugo then never asks the client for a new token: the connection
	// lives until the client leaves, it drops, or it is disconnected - with
	// the server API's Disconnect, or by Centrifugo PRO's user blocking or
	// token revocation. A reconnect presents the token again, so ExpiresAt
	// applies to it: past it, Centrifugo refuses the token, and the client
	// asks for a new one.
	ConnectionNoExpiry bool
	// IssuedAt is the "iat" claim. Zero means now.
	IssuedAt time.Time
	// ID is the token's "jti" claim, for Centrifugo PRO's token revocation.
	ID string
	// Audience and Issuer are the "aud" and "iss" claims, which Centrifugo
	// checks when configured to: client.token.audience and issuer.
	Audience string
	Issuer   string
	// Info is information about the connection, as JSON: it is part of
	// presence data, join and leave events and the client's publications.
	Info jsontext.Value
	// B64Info is binary info in base64, instead of Info.
	B64Info string
	// Channels are server-side channels to subscribe the connection to.
	Channels []string
	// Subs are server-side channels to subscribe the connection to, each with
	// its options. Set Channels or Subs, not both: Centrifugo ignores
	// Channels when Subs is set.
	Subs map[string]SubscribeOptions
	// Meta is custom data attached to the connection, as JSON. Centrifugo
	// never sends it to clients, but the token carries it, readable by the
	// client holding the token: put nothing secret in it - the connect proxy's
	// meta never reaches the client.
	Meta jsontext.Value
	// Caps are capabilities granting the connection operations in channels.
	// Centrifugo PRO only.
	Caps []ChannelsCapability
	// Labels are the connection's client labels: immutable, for metrics,
	// targeted server API calls and CEL expressions. Centrifugo PRO only.
	Labels map[string]string
}

// ChannelsCapability grants operations in channels: a connection may do an
// operation in a channel when any of its capabilities both allows the
// operation and matches the channel. Centrifugo PRO only.
type ChannelsCapability struct {
	// Channels are the channels, or channel patterns, it applies to.
	Channels []string `json:"channels,omitzero"`
	// Allow are the operations it grants: "sub" (subscribe), "pub" (publish),
	// "hst" (history) and "prs" (presence).
	Allow []string `json:"allow,omitzero"`
	// Match is how Channels are matched: empty for exact names, "wildcard" or
	// "regex".
	Match string `json:"match,omitzero"`
}

// SubscribeOptions are the options of a server-side subscription in
// [ConnectionClaims].Subs.
type SubscribeOptions struct {
	// Info is channel-specific information about the connection, as JSON.
	Info jsontext.Value `json:"info,omitzero"`
	// B64Info is binary info in base64, instead of Info.
	B64Info string `json:"b64info,omitzero"`
	// Data is sent to the client with the subscription.
	Data jsontext.Value `json:"data,omitzero"`
	// B64Data is binary data in base64, instead of Data.
	B64Data string `json:"b64data,omitzero"`
	// Override overrides channel options of the namespace for this
	// subscription.
	Override gocent.SubscribeOptionOverride `json:"override,omitzero"`
	// ServerTagsFilter makes the subscription receive only publications whose
	// tags match it. Centrifugo PRO only, which refuses a malformed filter:
	// the subscription fails. The zero filter is none.
	ServerTagsFilter gocent.FilterNode `json:"server_tags_filter,omitzero"`
}

// SubscriptionClaims are the claims of a subscription token. See
// https://centrifugal.dev/docs/server/channel_token_auth.
type SubscriptionClaims struct {
	// User is the ID of the user the connection belongs to: the "sub" claim.
	// It must be the connection's user.
	User string
	// Channel is the channel the token allows to subscribe to. Required.
	Channel string
	// ExpiresAt is until when the token is accepted: the "exp" claim. The
	// subscription expires then too, unless SubscriptionExpiresAt or
	// SubscriptionNoExpiry say otherwise: Centrifugo asks the client for a new
	// token, and ends the subscription without one. Required, unless NoExpiry
	// is set.
	ExpiresAt time.Time
	// NoExpiry leaves the "exp" claim out: the token is accepted forever, and
	// works forever once leaked. It must be set explicitly, instead of
	// ExpiresAt.
	NoExpiry bool
	// SubscriptionExpiresAt, when set, is when the subscription expires
	// instead of at ExpiresAt: the "expire_at" claim. Most applications leave
	// it unset.
	SubscriptionExpiresAt time.Time
	// SubscriptionNoExpiry makes the subscription never expire - the
	// "expire_at" claim zero - while ExpiresAt still bounds when the token is
	// accepted: with a short ExpiresAt, a token which subscribes once.
	// Centrifugo then never asks the client for a new token: the subscription
	// lives until the client unsubscribes, it is unsubscribed - with the
	// server API's Unsubscribe, or by Centrifugo PRO's token revocation - or
	// its connection ends. A resubscribe presents the token again, so
	// ExpiresAt applies to it.
	SubscriptionNoExpiry bool
	// IssuedAt is the "iat" claim. Zero means now.
	IssuedAt time.Time
	// ID is the token's "jti" claim, for Centrifugo PRO's token revocation.
	ID string
	// Audience and Issuer are the "aud" and "iss" claims, which Centrifugo
	// checks when configured to.
	Audience string
	Issuer   string
	// Info is channel-specific information about the connection, as JSON.
	Info jsontext.Value
	// B64Info is binary info in base64, instead of Info.
	B64Info string
	// Data is sent to the client in the subscribe reply.
	Data jsontext.Value
	// B64Data is binary data in base64, instead of Data.
	B64Data string
	// Override overrides channel options of the namespace for this
	// subscription.
	Override gocent.SubscribeOptionOverride
	// Allow are capabilities granted in the channel besides subscribing:
	// "pub", "hst" and "prs". Centrifugo PRO only.
	Allow []string
	// ServerTagsFilter makes the subscription receive only publications whose
	// tags match it. Centrifugo PRO only, which refuses a malformed filter:
	// the subscription fails. The zero filter is none.
	ServerTagsFilter gocent.FilterNode
}

// The claims as Centrifugo reads them.
type connectionWire struct {
	registered
	ExpireAt *int64                      `json:"expire_at,omitzero"`
	Info     jsontext.Value              `json:"info,omitzero"`
	B64Info  string                      `json:"b64info,omitzero"`
	Channels []string                    `json:"channels,omitzero"`
	Subs     map[string]SubscribeOptions `json:"subs,omitzero"`
	Meta     jsontext.Value              `json:"meta,omitzero"`
	Caps     []ChannelsCapability        `json:"caps,omitzero"`
	Labels   map[string]string           `json:"labels,omitzero"`
}

type subscriptionWire struct {
	registered
	Channel          string                         `json:"channel"`
	ExpireAt         *int64                         `json:"expire_at,omitzero"`
	Info             jsontext.Value                 `json:"info,omitzero"`
	B64Info          string                         `json:"b64info,omitzero"`
	Data             jsontext.Value                 `json:"data,omitzero"`
	B64Data          string                         `json:"b64data,omitzero"`
	Override         gocent.SubscribeOptionOverride `json:"override,omitzero"`
	Allow            []string                       `json:"allow,omitzero"`
	ServerTagsFilter gocent.FilterNode              `json:"server_tags_filter,omitzero"`
}

// registered are the claims of RFC 7519 Centrifugo reads.
type registered struct {
	Subject   string `json:"sub"`
	ExpiresAt int64  `json:"exp,omitzero"`
	IssuedAt  int64  `json:"iat,omitzero"`
	ID        string `json:"jti,omitzero"`
	Audience  string `json:"aud,omitzero"`
	Issuer    string `json:"iss,omitzero"`
}

func newRegistered(user string, expiresAt, issuedAt time.Time, id, audience, issuer string) registered {
	if issuedAt.IsZero() {
		issuedAt = time.Now()
	}
	r := registered{Subject: user, IssuedAt: issuedAt.Unix(), ID: id, Audience: audience, Issuer: issuer}
	if !expiresAt.IsZero() {
		r.ExpiresAt = expiresAt.Unix()
	}
	return r
}

// checkExpiry refuses claims which do not say when the token expires, or
// which contradict themselves.
func checkExpiry(expiresAt time.Time, noExpiry bool, sessionAt time.Time, sessionNever bool, session string) error {
	switch {
	case expiresAt.IsZero() && !noExpiry:
		return errors.New("token: set ExpiresAt, or NoExpiry for a token accepted forever")
	case !expiresAt.IsZero() && noExpiry:
		return errors.New("token: ExpiresAt and NoExpiry contradict each other")
	case !sessionAt.IsZero() && sessionNever:
		return fmt.Errorf("token: %sExpiresAt and %sNoExpiry contradict each other", session, session)
	}
	return nil
}

// expireAt is the "expire_at" claim: left out, zero for never, or a time.
func expireAt(at time.Time, never bool) *int64 {
	switch {
	case never:
		return new(int64(0))
	case at.IsZero():
		return nil
	}
	return new(at.Unix())
}

// Connection returns a connection token with c's claims, signed with key.
func Connection(key Key, c ConnectionClaims) (string, error) {
	if err := checkExpiry(c.ExpiresAt, c.NoExpiry, c.ConnectionExpiresAt, c.ConnectionNoExpiry, "Connection"); err != nil {
		return "", err
	}
	if len(c.Channels) > 0 && len(c.Subs) > 0 {
		return "", errors.New("token: set Channels or Subs, not both: Centrifugo ignores Channels when Subs is set")
	}
	return sign(key, connectionWire{
		registered: newRegistered(c.User, c.ExpiresAt, c.IssuedAt, c.ID, c.Audience, c.Issuer),
		ExpireAt:   expireAt(c.ConnectionExpiresAt, c.ConnectionNoExpiry),
		Info:       c.Info,
		B64Info:    c.B64Info,
		Channels:   c.Channels,
		Subs:       c.Subs,
		Meta:       c.Meta,
		Caps:       c.Caps,
		Labels:     c.Labels,
	})
}

// Subscription returns a subscription token with c's claims, signed with key.
// c.Channel is required.
func Subscription(key Key, c SubscriptionClaims) (string, error) {
	if c.Channel == "" {
		return "", errors.New("token: subscription token without a channel")
	}
	if err := checkExpiry(c.ExpiresAt, c.NoExpiry, c.SubscriptionExpiresAt, c.SubscriptionNoExpiry, "Subscription"); err != nil {
		return "", err
	}
	return sign(key, subscriptionWire{
		registered:       newRegistered(c.User, c.ExpiresAt, c.IssuedAt, c.ID, c.Audience, c.Issuer),
		Channel:          c.Channel,
		ExpireAt:         expireAt(c.SubscriptionExpiresAt, c.SubscriptionNoExpiry),
		Info:             c.Info,
		B64Info:          c.B64Info,
		Data:             c.Data,
		B64Data:          c.B64Data,
		Override:         c.Override,
		Allow:            c.Allow,
		ServerTagsFilter: c.ServerTagsFilter,
	})
}

func sign(key Key, claims any) (string, error) {
	if key.sign == nil {
		return "", errors.New("token: no key")
	}
	header := struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitzero"`
	}{key.alg, "JWT", key.kid}
	h, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	// Invalid JSON in Info, Data or Meta fails here, not in Centrifugo.
	p, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	enc := base64.RawURLEncoding
	signed := enc.EncodeToString(h) + "." + enc.EncodeToString(p)
	sig, err := key.sign([]byte(signed))
	if err != nil {
		return "", err
	}
	return signed + "." + enc.EncodeToString(sig), nil
}
