# Version History

## v0.0.25 — 2026-05-28

No exported API breaking changes. All changes below are either additive or are
behavior changes you'll observe at runtime but won't break compilation.

### Behavior changes (worth knowing)

- **`Server.Close()` now exits the dispatch loop.** Previously `Server.start()`
  ran forever after `Close` was called; the dispatch goroutine now exits via a
  new internal `done` channel. If you held a reference to the `*Server` after
  `Close` and expected its background goroutine to keep accepting `Broadcast`
  calls, that no longer works — `Broadcast` after `Close` is undefined.

- **`Server.Close()` now also closes the configured `StackExchange`** when the
  exchange implements the new optional `StackExchangeCloser` interface. The
  bundled redis and nats exchanges both implement it, so calling `Server.Close`
  now releases their background goroutines and broker connections — previously
  those leaked for the lifetime of the process.

- **`Conn.Close` no longer leaks a goroutine during server shutdown.** The
  internal goroutine that notifies `s.disconnect` now selects against the
  server's `done` channel.

- **`Server.Ask` no longer deadlocks when `ctx` fires while a replier is in
  flight.** The internal wait channel is now buffered to cap 1 with deferred
  cleanup. Previously a context cancellation racing with a reply could pin the
  reader goroutine inside `handleMessage` and stall further `Ask` calls.

- **`Conn.Connect` on the server side no longer sleeps in a 15ms poll loop**
  waiting for ack. It now blocks on a channel that closes the moment the client
  acknowledges. Behaviorally identical except faster and cheaper.

### Additive APIs (non-breaking)

- New optional `StackExchangeCloser` interface in the root package — implement
  `Close() error` on a custom `StackExchange` to participate in `Server.Close`.
- `(*redis.StackExchange).Close()` and `(*nats.StackExchange).Close()` — both
  idempotent, both safe to call multiple times.
- `NSConn.Broadcast` / `NSConn.BroadcastOthers` (added in commit `07c1e09` —
  documented in `Broadcast.md` for the first time in this release).

### Dependency floor

- Module `go 1.26` (was already at 1.26 — no change to the floor).
- Minimum supported Go version remains 1.21.
