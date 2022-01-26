# v3.2.0

* Fix broadcast request bug: JSON payloads were additionally encoded to base64 due to the lack of `json.RawMessage` usage. See [#16](https://github.com/centrifugal/gocent/pull/16).

# v3.1.0

* Add `Client.Subscribe` method to dynamically subscribe user to a channel (using server-side subscriptions).

```
gorelease -base v3.0.0 -version v3.1.0
github.com/centrifugal/gocent/v3
--------------------------------
Compatible changes:
- (*Client).Subscribe: added
- (*Pipe).AddSubscribe: added

v3.1.0 is a valid semantic version for this release.
```

# v3.0.0

HTTP API client for Centrifugo >= v3.0.0

* API address now should be passed explicitly, like `http://localhost:8000/api`. Previously `gocent` could automatically add `/api` for address like `http://localhost:8000` - this behaviour now removed.
* API changed to reflect Centrifugo v3 improvements - see [migration guide](https://centrifugal.dev/docs/getting-started/migration_v3) and [API description](https://centrifugal.dev/docs/server/server_api)

# v2.2.0

* Add `Config.GetAddr` function to dynamically provide API endpoint at the moment of API request
