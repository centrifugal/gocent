# Key rotation with JWKS

A backend issuing Centrifugo connection and subscription tokens, signed with
keys it rotates without restarting Centrifugo. The backend publishes the
public keys at a JWKS endpoint, and Centrifugo fetches a key by the token's
`kid` when it first sees it.

Keys are PEM private keys in a directory, each named by the time it starts
signing, in UTC:

- `2027-04-01.pem` signs from 00:00 UTC that day;
- `2027-04-01T15-30Z.pem` signs from 15:30 UTC (hyphens: file names can't
  always have colons).

Every key is published, those not active yet included. The key activated most
recently signs: each instance switches by itself when the next key's time
comes.

## Run it

```sh
go run . -genkey keys/2026-10-01.pem   # an Ed25519 key; RSA and ECDSA keys work too
go run . -keys keys                     # listens on :3000
```

Centrifugo, configured with the endpoint only - no keys of its own:

```sh
docker run --rm -p 8000:8000 \
  -e CENTRIFUGO_CLIENT_TOKEN_JWKS_PUBLIC_ENDPOINT=http://host.docker.internal:3000/centrifugo/jwks \
  -e CENTRIFUGO_UNI_HTTP_STREAM_ENABLED=true \
  centrifugo/centrifugo:v6.10.1 centrifugo
```

A token for the session's user, and a connection with it:

```sh
TOKEN=$(curl -s -X POST --cookie session=42 localhost:3000/centrifugo/connection_token | jq -r .token)
curl -X POST localhost:8000/connection/uni_http_stream -d "{\"token\":\"$TOKEN\"}"
```

`sessionUser` and `canRead` in `main.go` stand for the application's own
authentication and permission checks.

## Rotate

1. **Add the next key, dated ahead** - a day or more for a routine rotation -
   and roll out the backend. Every instance publishes it; the current key keeps
   signing.

   ```sh
   go run . -genkey keys/2027-04-01.pem
   ```

2. **Nothing**: at its time, every instance signs with the new key. By then
   every instance publishes it, so Centrifugo finds it whichever instance it
   asks.
3. **Retire** the old key once the tokens it signed have expired - 15 minutes
   here, the tokens' `ExpiresAt` - by removing its file and rolling out.
   Centrifugo drops it when its cached keys expire.

Date a key further ahead than a rollout takes: a key activating while some
instances don't publish it yet makes Centrifugo refuse its tokens until it
fetches the keys again - for 5 seconds at least. Centrifugo then makes clients
get a new token and retry, but a key dated ahead avoids the refusals.

**After a key leak**, rotate fast, in two rollouts:

1. Add a new key named by a time - minutes ahead, more than a rollout takes:

   ```sh
   go run . -genkey keys/2027-04-01T15-30Z.pem
   ```

2. Once that time has passed - every instance signs with the new key - remove
   the leaked key's file and roll out. Centrifugo drops the key when its cached
   keys expire - an hour up to Centrifugo v6.10, minutes in later versions -
   and tokens the leaked key signed stop working then.

The backend refuses to start when no key is active yet, when a file is named
by something else than a time, or when two keys activate together. Every
instance must have the same key files.
