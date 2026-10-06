# neffos examples

Runnable programs for [neffos](https://github.com/kataras/neffos), ordered the way you would learn it: a tutorial that grows one chat app step by step, then one section per topic. Every example is a single `main.go` (plus an `index.html` where a browser page helps) that opens with a comment saying what it shows, what you will be able to do afterwards, how to run and try it, and which example to read next. Every section names the [wiki](https://github.com/kataras/neffos/wiki) page it pairs with.

Requirements: Go 1.27 or newer. A few examples need more, and say so in a `Requires:` line: Redis or NATS for scale-out, Docker for its compose file, `protoc` only to regenerate the protobuf code. New to neffos? Start with `01-getting-started` and do the steps in order.

## Running an example

All examples but one share one module, `_examples/go.mod`, which points `github.com/kataras/neffos` at the code in this repository; the exception is `09-integrations/iris-jwt`, explained in its section. In a clone, run an example from its folder:

```sh
cd _examples/01-getting-started/01-echo
go run main.go server          # terminal 1
go run main.go client alice    # terminal 2
```

In your own module, copy the files and fetch the released library:

```sh
mkdir myapp && cd myapp
go mod init myapp
go get github.com/kataras/neffos@latest
# copy the example's main.go (and index.html, if it has one) here, then:
go run . server
```

Every example listens on `:8080` with its websocket endpoint at `/ws`. The exceptions are the two scale-out examples, which run a second server on `:9090`, and the load tests, which use `:9595`. A program with a Go client takes `server` or `client <name>` as its first argument, after any flags.

## 01. Getting started: the Lobby tutorial

Twelve programs that grow one app, the Lobby (a chat with a namespace, rooms, private messages and an operator console), from a single echo event to a tested server with a browser client. Each step is a complete `main.go` that adds one idea to the previous one, keeping the same function names and order (`main`, `newServer`, `runServer`, `runClient`, `serverEvents`, `clientEvents`), so you can diff two neighbours to see exactly what changed. Do them in order; afterwards the other sections stand alone as references. You should be comfortable with Go and know what a websocket is. No neffos experience is assumed.

Wiki: [Getting started](https://github.com/kataras/neffos/wiki/Getting-started), then [Namespaces](https://github.com/kataras/neffos/wiki/Namespaces), [Rooms](https://github.com/kataras/neffos/wiki/Rooms) and [Broadcast](https://github.com/kataras/neffos/wiki/Broadcast).

| Step | Shows |
| --- | --- |
| [01-echo](01-getting-started/01-echo) | The smallest server and Go client: `neffos.New`, `gorilla.DefaultUpgrader`, `Events`, `neffos.Reply`, `Dial`, `Connect`, `Emit`, `NotifyClose` |
| [02-namespaces](01-getting-started/02-namespaces) | A `chat` namespace, the user name as the connection ID through `IDGenerator`, and every lifecycle hook logged |
| [03-broadcast](01-getting-started/03-broadcast) | Relaying to everyone else with `BroadcastOthers`, operator notices with `Server.Broadcast`, private messages with `Message.To`, `Exclude` |
| [04-rooms](01-getting-started/04-rooms) | `/join` and `/leave`, room-scoped chat, a `staff` room gated in `OnRoomJoin`, `IsForced` when a connection drops |
| [05-encoding](01-getting-started/05-encoding) | JSON bodies with `Marshal`, `Unmarshal` and `MessageObjectMarshaler`; binary frames with `EmitBinary` and `SetBinary` |
| [06-ask-and-errors](01-getting-started/06-ask-and-errors) | `NSConn.Ask` and `Server.Ask` with context deadlines, `Message.Err`, `RegisterKnownError` and `errors.Is` across the wire |
| [07-authentication](01-getting-started/07-authentication) | A bearer token checked by HTTP middleware (401) and by `OnConnect`, the user kept with `Conn.Set` and `Conn.Value` |
| [08-timeouts-and-limits](01-getting-started/08-timeouts-and-limits) | `WithTimeout`: read and write deadlines, a heartbeat with `PingInterval`, `MaxMessageSize`; `Send` errors sorted with `IsTimeoutError` and `IsDisconnectError` |
| [09-close-and-shutdown](01-getting-started/09-close-and-shutdown) | Kicking a user with `Conn.Terminate(4000, reason)`, reading `CloseStatus(c.Err())` on both sides, and `Server.Shutdown` on Ctrl+C |
| [10-struct-handler](01-getting-started/10-struct-handler) | The server's handlers as a per-connection struct: `NewStruct`, `EventTrimPrefixMatcher`, `JoinConnHandlers`, and the limits through `SetTimeouts`, `SetPingInterval`, `SetMaxMessageSize` |
| [11-testing](01-getting-started/11-testing) | `main_test.go`: `httptest.NewServer(newServer())`, two Go clients per test, channel and `select` assertions, close codes |
| [12-browser-client](01-getting-started/12-browser-client) | The same chat from the browser: an `index.html` embedded with `//go:embed`, neffos.js `dial` with reconnection, `connect`, `emit`, `ask`, `joinRoom`, `emitBinary` |

## 02. Backends

neffos runs on gorilla/websocket, gobwas/ws or coder/websocket; the events code is the same on all three.

Wiki: [Upgraders and Dialers](https://github.com/kataras/neffos/wiki/Upgraders-and-dialers), [Choosing a backend](https://github.com/kataras/neffos/wiki/Choosing-a-backend).

| Example | Shows |
| --- | --- |
| [choose-backend](02-backends/choose-backend) | One program, three backends: a `-backend gorilla\|gobwas\|coder` flag picks the `DefaultUpgrader` and `DefaultDialer` pair; any client talks to any server |
| [custom-options](02-backends/custom-options) | Each library's own options side by side: origin checks (`CheckOrigin`, `OriginPatterns`, a middleware for gobwas), a subprotocol, buffer sizes, handshake headers |
| [socket-wrapper](02-backends/socket-wrapper) | A frame-counting `Socket` wrapper through `Server.Upgrade` that passes on `SocketCloser`, `SocketPinger` and `SocketReadLimiter` |

## 03. Messaging

Wiki: [Encoding](https://github.com/kataras/neffos/wiki/Encoding), [Binary messages](https://github.com/kataras/neffos/wiki/Binary-messages), [Protobufs](https://github.com/kataras/neffos/wiki/Protobufs), [Native messages](https://github.com/kataras/neffos/wiki/Native-messages), [Errors](https://github.com/kataras/neffos/wiki/Errors).

| Example | Shows |
| --- | --- |
| [protobuf](03-messaging/protobuf) | Protocol Buffers bodies sent as binary frames with `EmitBinary`, a `.proto` file and its generated Go code |
| [native-messages](03-messaging/native-messages) | Plain websocket clients (a browser `WebSocket`, websocat) through `OnNativeMessage` and `Message.IsNative` |
| [known-errors](03-messaging/known-errors) | Error values shared by server and client with `RegisterKnownError` and `errors.Is`, a `ResolveError` method for error texts that carry data, and `CloseError{Code: 4001}` returned from an event |

## 04. Handlers

Wiki: [Struct handlers](https://github.com/kataras/neffos/wiki/Struct-handlers), [Namespaces](https://github.com/kataras/neffos/wiki/Namespaces).

| Example | Shows |
| --- | --- |
| [compose-handlers](04-handlers/compose-handlers) | Building handlers in pieces: `Events.On`, `Namespaces.On`, `JoinConnHandlers` and the catch-all `OnAnyEvent` |
| [struct-injector](04-handlers/struct-injector) | `Struct.SetInjector` builds each connection's controller with its dependencies; `EventPrefixMatcher`; two controllers in two namespaces |

## 05. Connections

Wiki: [Connections](https://github.com/kataras/neffos/wiki/Connections).

| Example | Shows |
| --- | --- |
| [users-and-devices](05-connections/users-and-devices) | One user with several connections: a registry keyed by user that sends to all of their devices |
| [rate-limit](05-connections/rate-limit) | A one-second window with `Conn.Increment` and `Conn.Decrement`; a client that floods is closed with `ClosePolicyViolation` and a reason |
| [inspect-and-do](05-connections/inspect-and-do) | An operator console over live connections: `GetConnections`, `GetConnectionsByNamespace`, `Server.Do`, `Conn.DisconnectAll`, `FireDisconnectAlways`, `ReconnectTries` |

## 06. Server push

Wiki: [Broadcast](https://github.com/kataras/neffos/wiki/Broadcast).

| Example | Shows |
| --- | --- |
| [http-to-websocket](06-server-push/http-to-websocket) | `POST /notify?user=` pushes to one user, or to everyone without `user`, with `Server.Broadcast` and `Message.To` |
| [cron-notifications](06-server-push/cron-notifications) | A scheduled job (robfig/cron) delivering pending notifications to the users who are online |

## 07. Scale out

Wiki: [Scale out](https://github.com/kataras/neffos/wiki/Scale-out), [Redis](https://github.com/kataras/neffos/wiki/Scale-out-using-redis), [Nats](https://github.com/kataras/neffos/wiki/Scale-out-using-nats), [Writing a custom StackExchange](https://github.com/kataras/neffos/wiki/Custom-StackExchange).

| Example | Shows |
| --- | --- |
| [redis-or-nats](07-scale-out/redis-or-nats) | Two servers acting as one through Redis or NATS: `-addr` and `-exchange` flags, a browser page, a Dockerfile and `compose.yaml` |
| [custom-stackexchange](07-scale-out/custom-stackexchange) | A `StackExchange` of your own, with `StackExchangeInitializer` and `StackExchangeCloser`: an in-process bus joins two servers, `Server.Ask` crosses them, and a test checks both |

## 08. Load testing

Each program prints counts and timings; run them on a quiet machine.

Wiki: [Architecture](https://github.com/kataras/neffos/wiki/Architecture).

| Example | Shows |
| --- | --- |
| [server](08-load-testing/server) | A server that counts connections and reports leftovers and memory; pick gobwas, gorilla or coder |
| [clients](08-load-testing/clients) | Many short-lived Go clients sending `test.data`, with an optional cap on open connections |
| [broadcast](08-load-testing/broadcast) | One process, a thousand clients, `Server.Broadcast` every two seconds until every message arrived |

## 09. Integrations

Wiki: [Authentication](https://github.com/kataras/neffos/wiki/Authentication).

| Example | Shows |
| --- | --- |
| [iris-jwt](09-integrations/iris-jwt) | neffos inside an Iris v12 app: `websocket.Handler` behind the `middleware/jwt` verifier, the verified user read in events with `websocket.GetContext` and `jwt.Get` |

iris-jwt is its own Go module (`09-integrations/iris-jwt/go.mod`) because it follows the public Iris release: the websocket package of Iris v12.2.11 tracks neffos v0.0.x until the next Iris release, so it builds against that neffos version and not against this repository. Run it from its folder; `go vet` and `go test` from `_examples` do not enter it.

## The JavaScript client

Browsers talk to neffos through [neffos.js](https://github.com/kataras/neffos.js). The pages in these examples load it from a CDN and use the global `neffos`:

```html
<script src="https://cdn.jsdelivr.net/npm/neffos.js@0.3/dist/neffos.global.min.js"></script>
<script type="module">
  const conn = await neffos.dial("ws://localhost:8080/ws?name=alice", {
    chat: { Chat: (nsConn, msg) => console.log(msg.text()) },
  }, { reconnect: 2000 });
  const chat = await conn.connect("chat");
  chat.emit("Chat", "hello");
</script>
```

Browsers cannot set handshake headers, so names and tokens travel as query parameters (`?name=`, `?token=`); the Go servers here read both. With a bundler or TypeScript, `import * as neffos from "neffos.js"`. The neffos.js repository has its own examples for Node.js.

## Where to go next

- The [wiki](https://github.com/kataras/neffos/wiki) explains the concepts behind these examples; every section above names its pages. Coming from v0.0.x? Read [Migrating to v0.1.0](https://github.com/kataras/neffos/wiki/Migrating-to-v0.1.0).
- The [API reference](https://pkg.go.dev/github.com/kataras/neffos) on pkg.go.dev lists every type and method.
- Questions and bug reports go to [GitHub issues](https://github.com/kataras/neffos/issues).
