# CLAUDE.md

See [AGENTS.md](AGENTS.md). It is the guide for working on this package and
applies here in full: the invariants, how to run the suite, how to update to a
new Centrifugo API, and the list of things that look wrong but are deliberate.

Three points from it are worth repeating, because they are the ones most easily
lost:

- **Never edit `api_gen.go`.** Change `api.proto` or `internal/gen`, then run
  `make generate`.
- **Broadcast and batch return an error together with a complete result** when
  one or more of their parts failed - up to all of them. Every other call
  returns one or the other.
- **Automatic batching never delays a call below `MaxInFlight`** and never
  gives a caller a reply that is not its own.

Run `make check` before reporting a change as done, and `make test-integration`
with Centrifugo running for a change to the transport, errors or batching.
