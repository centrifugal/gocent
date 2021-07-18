# v3.0.0

HTTP API client for Centrifugo >= v3.0.0

* API address now should be passed explicitly, like `http://localhost:8000/api`. Previously `gocent` could automatically add `/api` for address like `http://localhost:8000` - this behaviour now removed.
* API changed to reflect Centrifugo v3 improvements

# v2.2.0

* Add `Config.GetAddr` function to dynamically provide API endpoint at the moment of API request
