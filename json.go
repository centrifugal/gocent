package gocent

import "encoding/json/jsontext"

// jsonOptions are the options of every encoding of a request and decoding of
// a reply: an object may repeat a name. Payloads are the application's, and
// whether one may repeat a key is Centrifugo's to decide, not the client's -
// and a reply holding a publication someone else made with a repeated key is
// still read.
var jsonOptions = jsontext.AllowDuplicateNames(true)
