# Changelog

### Looking for previous versions?

    https://github.com/kataras/neffos/releases

### Should I upgrade my Neffos?

Developers are not forced to upgrade if they don't really need it. Upgrade whenever you feel ready.

**How to upgrade**: Open your command-line and execute this command: `go get github.com/kataras/neffos@latest` and `go mod tidy`.

# Next

### Highlights

- Lifecycle and concurrency fixes across `Conn.Ask`, `Server.Close` and the new `Server.Shutdown` remove several
  ways a connection, or a whole server, used to hang.
- An additive API for production use: heartbeats, a message size cap, `Conn.Terminate` with a close code,
  `Server.Shutdown`, and close-status helpers, none of which change an existing signature.
- Three websocket backends behind the same `Socket` interface (`gorilla`, `gobwas`, and the new `coder`), plus
  `stackexchange/redis` rewritten on go-redis v9 and `stackexchange/nats` rewritten to use two connections
  total instead of one per websocket connection.
- Go 1.27 is the floor, and the API uses what it brings: generic methods (`msg.As[T]()`, `c.Value[T](key)`,
  `c.AskObject[Reply](...)`, `c.Instance[T]()`), `encoding/json/v2` for message bodies, the standard library's
  `uuid` package, and a `Struct[T]` handler whose injector is typed. `reflect` is gone from the API except for
  `StructValue`, the escape hatch frameworks such as iris need.

### Breaking changes

**Go 1.27 or later is required** (`go 1.27` in `go.mod`). Everything below is a compile-time break; the fix for
each is in the right column. The wire protocol is unchanged, so v0.0.x servers, v0.1.0 servers and neffos.js
0.3 clients keep talking to each other.

| v0.0.x | v0.1.0 |
|---|---|
| `neffos.Marshal(v) []byte` | `neffos.Marshal(v) ([]byte, error)`; or `c.SendObject(event, v)`, `room.SendObject(event, v)`, `c.AskObject[Reply](ctx, event, v)`, `neffos.ReplyObject(v)` |
| `msg.Unmarshal(&v)` | unchanged; or `v, err := msg.As[T]()` |
| `neffos.DefaultMarshaler`, `neffos.DefaultUnmarshaler` (`encoding/json` v1 function values) | typed `neffos.Marshaler` and `neffos.Unmarshaler` on `encoding/json/v2`, configured by `neffos.JSONMarshalOptions` and `neffos.JSONUnmarshalOptions` |
| `c.Conn.Get(key) any` | `v, ok := c.Conn.Value[T](key)`; `Value[any]` is the old `Get` |
| `neffos.NewStruct(ptr any) *Struct` | `neffos.NewStruct(prototype *T) *Struct[T]`, same call syntax |
| `neffos.NewStruct(reflect.Value)` | `neffos.NewStructValue(reflect.Value) *StructValue` |
| `Struct.SetInjector(neffos.StructInjector)` | `Struct[T].SetInjector(func(*NSConn) *T)`; `StructValue.SetInjector(func(*NSConn) reflect.Value)` |
| `type StructInjector` | removed |
| `Server.Broadcast(fmt.Stringer, ...)`, `neffos.Exclude(id) fmt.Stringer` | `Server.Broadcast(neffos.Sender, ...)`, `neffos.Exclude(id) neffos.Sender`; `*Conn`, `*NSConn` and `*Room` are Senders, call sites do not change |
| `neffos.EnableDebug(printer any)` | `neffos.EnableDebug(neffos.Printer)`; `*log.Logger` qualifies, wrap anything else in `neffos.PrinterFunc` |
| `neffos.DebugEach(any, any)` | removed |
| `var EventPrefixMatcher`, `var EventTrimPrefixMatcher` | funcs, same call syntax |
| `github.com/google/uuid` | the standard library's `uuid`; connection ids are still UUID v4 strings |

- `neffos.Marshal` used to write the error text into the returned body when encoding failed, so a broken value
  went out as a message that said `json: unsupported type`; now it returns the error and a nil body.
- Typed `MessageType` constants and `const` event names: code that compared an untyped `int` to
  `neffos.TextMessage` or assigned to an event-name variable stops compiling; no such code was found in the
  repository or examples.
- Message bodies are encoded with `encoding/json/v2`. For structs this changes a few things on the wire, see
  "Behaviour notes" below for the list and the opt-outs.

### Fixed

#### Connection lifecycle

- `Server.Close` used to leave its dispatch loop running forever; it now exits through an internal `done` channel.
- `Server.Close` now also closes the configured `StackExchange` when it implements `StackExchangeCloser`, instead
  of leaking its background goroutines and broker connections for the life of the process.
- `Conn.Close` no longer leaks a goroutine during server shutdown.
- `Server.Ask` no longer deadlocks when its context is cancelled while a reply is in flight.
- `Conn.Connect` on the server side no longer polls every 15ms waiting for the client's ack; it blocks on a
  channel that closes the moment the ack arrives.
- `Conn.Ask` now returns promptly once the connection closes, instead of hanging on the old `TestAsk` case.
- `Conn.Ask` no longer writes anything when called with a context that is already cancelled.
- `Conn.Ask` called from inside an event handler no longer hangs or returns the wrong reply when an unrelated
  message arrives first.
- `Conn.Increment` and `Conn.Decrement` fixed a race that dropped counts under concurrent calls on the same
  connection.
- `Conn.WaitConnect` now returns as soon as the connection closes, instead of polling on a fixed interval.
- A race in the internal one-shot waiter used by the connect, join and leave handshake could leave a caller
  blocked forever; fixed, and now covered by its own test.
- `Conn.DisconnectAll` and `NSConn.LeaveAll` no longer run user callbacks or make an `Ask` while holding an
  internal lock, which used to be able to deadlock.
- The async broadcaster used by `Server.Broadcast` no longer drops messages for a connection that is still
  writing a previous broadcast: 49 of 50 broadcasts were lost in the reproduction before the fix.
- `Server.Broadcast`'s internal batch delivery no longer skips the rest of a batch after one target in the batch
  does not match a given connection; only that one message is now skipped.

#### Server

- `ErrServerClosed` is now exported (it was an unexported `errServerClosed`).
- `Server.GetConnections` and `Server.GetConnectionsByNamespace` no longer panic with a concurrent map
  iteration error when called while connections are still being accepted.
- `Server.Close` now fires `OnDisconnect` for every connection exactly once; before, a connection that closed on
  its own at the same moment as `Close` could be missed entirely.
- `Server.Close` and `Server.Shutdown` now terminate connections concurrently, so one slow peer cannot stall the
  rest; a 20-connection test went from about 4 seconds to about 200ms.
- `Server.Do`, `Server.Broadcast` (with a `SyncBroadcaster`) and `Server.Ask` now return promptly once the
  server is closed, instead of blocking.

#### Backends

- The `gorilla` and `gobwas` sockets now implement `SocketCloser`, `SocketPinger` and `SocketReadLimiter`, so
  close codes, the heartbeat and `WithTimeout.MaxMessageSize` work on both.
- `gobwas`: fixed a pong frame interleaving with an in-flight data frame, which corrupted the data frame on the
  wire.
- `gobwas`: fixed its `Upgrader` and `Dialer` dropping bytes already buffered during the HTTP handshake, which
  could lose a frame the peer sent in the same TCP segment as the handshake request or response.
- A connection that ends with a clean FIN and no close frame now reports close code 1006
  (`CloseAbnormalClosure`) consistently across `gorilla`, `gobwas` and the new `coder` backend; a hard TCP
  reset still reports -1 (no code) on all three.

#### Stack exchanges

- `stackexchange/redis` is rewritten on `github.com/redis/go-redis/v9`, replacing
  `github.com/mediocregopher/radix/v3`; `Close` no longer leaks a goroutine per websocket connection.
- `stackexchange/redis`: `OnConnect`, `Subscribe` and `Unsubscribe` are now bounded by the configured dial
  timeout instead of hanging against a silent redis.
- `stackexchange/nats` now holds two nats connections for its whole life (one publisher, one subscriber) instead
  of one connection per websocket connection plus one per `Ask`, which fixes a connection and goroutine leak
  under load.
- `stackexchange/nats`: `Close` is now idempotent and returns the first error instead of ignoring later calls.

#### Panics removed

- `reflect.go`'s internal zero-value check no longer panics with an index-out-of-range error on a method with no
  return value (surfaced through `Struct`-based handlers).
- `Marshal(nil)` now returns a nil body, and `Message.Unmarshal(nil)` now returns an error, instead of both
  panicking.
- `EnableDebug` falls back to the default logger for an unsupported printer instead of panicking.
- `NSConn.Broadcast` and `NSConn.BroadcastOthers` no longer panic when called on a client-side connection; they
  do nothing there, as documented.
- `stackexchange/redis` and `stackexchange/nats` no longer panic when a message is sent to an empty namespace.

### Added

- `SocketCloser`, `SocketPinger` and `SocketReadLimiter`: optional interfaces a `Socket` can implement to get
  close codes, a heartbeat and a message size cap.
- `CloseStatus(err) int`: reads the close code out of an error chain, or -1 if there is none.
- Typed close-code constants, `CloseNormalClosure` through `CloseTLSHandshake` (gorilla's set, 14 values).
- `ErrMessageTooBig`: the error `Conn.Err` reports after the remote side sent a message over
  `WithTimeout.MaxMessageSize`.
- `MessageType` is now a typed `uint8` with named constants `TextMessage` and `BinaryMessage`, plus a
  `MessageType.String()` method.
- `Conn.Send`: sends a message and returns an error explaining why, instead of the old boolean `Write`.
- `Conn.Err() error`: the connection's close error, or nil while open.
- `Conn.Terminate(code, reason)`: closes the connection with a given close code and reason; `Conn.Close` is now
  `Terminate(CloseNormalClosure, "")`.
- `WithTimeout` gains `PingInterval` (heartbeat) and `MaxMessageSize` (read cap) fields.
- `Struct.SetPingInterval` and `Struct.SetMaxMessageSize`: the same two settings for a `Struct`-based handler.
- `NSConn.Send` and `Room.Send`: send an event to a specific namespace or room and return an error.
- `Server.Shutdown(ctx) error`: closes the server like `Close`, then waits for every connection's reader
  goroutine (and any event callback still running) to return, or until `ctx` is done.
- `Client.Conn() *Conn`: the client-side connection, for reading `Conn.Err` or calling `Conn.Terminate`.
- `StackExchangeCloser`: an optional interface for a `StackExchange`; its `Close` runs as part of `Server.Close`.
- `NSConn.Broadcast` and `NSConn.BroadcastOthers`: send to every connection, or every connection but the sender,
  from inside an event callback.
- `coder` package: a third backend, for `github.com/coder/websocket`, with `DefaultDialer`, `DefaultUpgrader`,
  `Dialer`, `Upgrader` and `Socket`, implementing the same `Socket` contract as `gorilla` and `gobwas`.
- `IsCloseError` now also recognises `net.ErrClosed`, wrapped or not.
- `IsTimeoutError` now also recognises `context.DeadlineExceeded` and `os.ErrDeadlineExceeded`, wrapped or not.
- `CloseError` gains a `Reason string` field and an `Unwrap() error` method.
- `Message.As[T]() (T, error)`: decodes the body into a new T. A `*T` that implements `MessageObjectUnmarshaler`
  decodes through its own method, as with `Unmarshal`.
- `NSConn.SendObject`, `Room.SendObject`: `Send` for a value, encoded with `Marshal`. `NSConn.AskObject[Reply]`:
  `Ask` for values, encoding the request and decoding the reply. `ReplyObject(v)`: `Reply` for a value; an
  encoding failure is returned as the event's error instead of being sent as a body.
- `Marshaler` and `Unmarshaler` function types for `DefaultMarshaler` and `DefaultUnmarshaler`, and
  `JSONMarshalOptions` / `JSONUnmarshalOptions`, the `encoding/json/v2` options they use.
- `Conn.Value[T](key) (T, bool)`: typed read of the connection store; false when the key is absent or holds
  another type.
- `Struct[T]` with `NewStruct(prototype *T)`: the struct handler is generic, so `SetInjector` takes a
  `func(*NSConn) *T` and the `*NSConn` field is set by neffos after the injector returns.
- `NSConn.Instance[T]() (*T, bool)`: the per-connection instance of a dynamic `Struct[T]`, for code outside the
  struct's own methods (an `OnNamespaceDisconnect` registered elsewhere, an audit hook).
- `StructValue` with `NewStructValue(reflect.Value)`: the reflection-driven form of `Struct`, for frameworks that
  only have the controller as a `reflect.Value`. This is what iris's mvc websocket controller will use.
- `Sender`: the sealed interface `Server.Broadcast` takes for the connection to skip. `*Conn`, `*NSConn`, `*Room`
  and `Exclude(id)` implement it.
- `Printer` and `PrinterFunc`: what `EnableDebug` writes to.

### Behaviour notes

- Message bodies are encoded and decoded with `encoding/json/v2`. Kept from v1 on purpose: map keys are sorted,
  invalid UTF-8 in a string is replaced by U+FFFD instead of failing the message, `time.Duration` is an int64
  nanosecond count, and member names match case-insensitively on decode. Changed, because v2 changed it: a nil
  slice encodes as `[]` (was `null`) and a nil map as `{}`; `omitempty` omits empty JSON values only, use
  `omitzero` for Go zero values (a `0`, `false` or zero `time.Duration` that `omitempty` used to drop is now
  sent); `<`, `>` and `&` are no longer HTML-escaped; a `[N]byte` encodes as base64; duplicate object names and
  invalid UTF-8 in an incoming body are rejected, so `msg.Unmarshal` and `msg.As` return an error where v1
  silently took the last value. To restore a v1 behaviour, reassign the options, for example
  `neffos.JSONMarshalOptions = json.JoinOptions(neffos.JSONMarshalOptions, json.FormatNilSliceAsNull(true))`.
  `encoding/json.RawMessage` is an alias of `jsontext.Value` since Go 1.27, so raw-JSON fields work with both.
- `Server.Broadcast` with `neffos.Exclude(connID)` or a `*Room` used to be a no-op under a `StackExchange`: the
  exclusion travelled in a field that `Message.Serialize` does not write, and every broadcast is serialized on
  its way through redis or nats. Both now resolve to the connection's server id and travel like a `*Conn` does.
  An id this server does not know (a connection on another node) is still excluded locally only.
- A dynamic `Struct` (one with a `*NSConn` field) binds its event methods once per connection, at
  `OnNamespaceConnect`. Each event used to do a `reflect.Value.Method(i).Interface()` lookup; it is now a plain
  function call (139 ns and one 8-byte allocation per event in `BenchmarkStructDynamicEvent`, all of it the
  `Message` copy).
- Unexported fields of a dynamic `Struct` prototype are no longer copied into the per-connection instance. They
  used to be reported as non-zero and `reflect.Value.Set` panicked on the first connect; a `sync.Mutex` or a
  private counter on the controller now just starts zero. Use `SetInjector` to fill private fields.

- A close frame now actually goes out: code 1000 on `Conn.Close` or a plain `Conn.Terminate`, and code 1001
  (`CloseGoingAway`) on `Server.Close` or `Server.Shutdown`.
- An `Ask` made inside an event handler dispatches any unrelated frames that arrive while it waits, running
  their handlers before its own reply arrives; at most one goroutine reads the socket at a time, but an `Ask`
  from a second goroutine while the reader is busy can still run a handler concurrently with it.
- `Server.Close` fires `OnDisconnect` for every connection exactly once, even one that closed on its own at the
  same moment.
- `JoinConnHandlers` keeps every input's settings: timeouts, `PingInterval` and `MaxMessageSize` combine, and a
  later non-zero value wins over an earlier one.
- Backpressure: a connection that stops reading holds its pending broadcasts in memory until its write deadline
  or its heartbeat closes it; there is no separate cap.
- `OnAnyEvent` also receives the connection's lifecycle events (connect, disconnect, room join and leave, not
  only the events it was registered for); an error returned from it refuses the namespace connect or room join
  that was routed to it.
- `Conn.Ask` on a closed connection returns an empty `Message` and an error, instead of echoing the input back.
- A plain error returned from `Server.OnConnect` now closes the handshake with code 1008
  (`ClosePolicyViolation`) and the error text as the reason; `ErrServerClosed` closes it with code 1001
  (`CloseGoingAway`).
- A `CloseError` returned from any event callback now closes the connection with its code. Before v0.1.0 this
  was documented but not done: only an error reply was sent.
- `StackExchange.OnDisconnect` now fires for every removed connection, including one that `Server.OnConnect`
  rejected.
- nats subjects are sanitised: a namespace containing `.`, `*`, `>` or whitespace now maps to `<prefix>.a_b`
  rather than `<prefix>.a.b`, so v0.0.x and v0.1.0 servers sharing one nats no longer share such namespaces.
  The redis exchange moved from PSUBSCRIBE to SUBSCRIBE, so namespaces with glob characters now work; channel
  names for ordinary namespaces are unchanged.
- During `Server.Close` and `Server.Shutdown` a namespace or room is already removed when its forced
  `OnNamespaceDisconnect` or `OnRoomLeave` fires, so `c.Conn.Namespace(x)` and `ns.Room(x)` return nil inside
  those callbacks.
- During `Server.Close` and `Server.Shutdown`, `Server.OnDisconnect` may run on the closing goroutine
  concurrently with the dispatch loop; protect shared state in the callback.

### Dependencies

- `github.com/redis/go-redis/v9` (v9.23.0) replaces `github.com/mediocregopher/radix/v3`.
- `github.com/nats-io/nats.go` bumped to v1.54.0.
- `github.com/coder/websocket` v1.8.15 added, for the new `coder` backend.
- `github.com/gorilla/websocket` v1.5.3 and `github.com/gobwas/ws` v1.4.0 are unchanged.
- `golang.org/x/sync` v0.23.0, `golang.org/x/crypto` v0.57.0 and `golang.org/x/sys` v0.48.0.
- `github.com/google/uuid` is removed; the standard library's `uuid` package (Go 1.27) generates the ids.
- Module floor raised to Go 1.27 (`go 1.27` in `go.mod`, no `toolchain` line). Notes that come with the
  toolchain itself: `go test` now runs the `stdversion` vet check by default, and the compiler may give two
  identical function literals one code pointer, so never compare event handlers by function value.

### Examples and documentation

- `_examples` is restructured into a numbered tutorial (`01-getting-started`, steps 01 to 12) plus topic folders
  (`02-backends` through `09-integrations`), each `main.go` with its own Learn, Run, Try and Next sections. See
  `_examples/README.md` for the full index.
- `_examples/09-integrations/iris-jwt` targets Iris v14 (`github.com/kataras/iris/v14`, with its
  `middleware/websocket` and `middleware/jwt`) and is its own module, so the shared examples module stays free of
  the Iris dependency tree.
- The migration notes for existing v0.0.x code live on the wiki:
  https://github.com/kataras/neffos/wiki/Migrating-to-v0.1.0

### Upgrading

See the wiki page for the full migration guide:
https://github.com/kataras/neffos/wiki/Migrating-to-v0.1.0

### neffos.js pairing

neffos.js 0.3.0 ships ESM, CommonJS and browser (global) builds from one source, with `ws` as an optional
dependency that is only imported dynamically when the build needs it (so a browser or Deno bundle never pulls
it in). It speaks the same wire protocol as both neffos (Go) v0.0.x and v0.1.x.
