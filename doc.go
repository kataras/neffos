// Package neffos is a cross-platform real-time framework with an
// expressive, elegant API, written in Go.
//
// # Overview
//
// A neffos application is a server and one or more clients exchanging
// Messages over a websocket connection. Messages are grouped into
// namespaces, and optionally into rooms within a namespace. The server and
// the client share the same handler types, so one set of event handlers can
// run on either side.
//
// # Server
//
// New builds a Server from an Upgrader and a ConnHandler:
//
//	upgrader := gorilla.DefaultUpgrader
//	server := neffos.New(upgrader, events)
//	http.Handle("/", server)
//
// Server implements http.Handler, so it can be mounted at a specific path,
// wrapped by middleware, or served next to an ordinary HTTP mux. Once it is
// running, Server.Broadcast, Server.Do, Server.Ask, Server.GetConnections
// and Server.GetConnectionsByNamespace are all safe to call from any
// goroutine, including from inside an event handler. One limit applies to
// Server.Ask there: an Ask aimed at the connection whose handler is running
// cannot be answered until that handler returns, so give its context a
// deadline.
//
// # Client
//
// Dial connects to a neffos server and returns a Client:
//
//	client, err := neffos.Dial(ctx, gorilla.DefaultDialer, "ws://localhost:8080", events)
//
// Client.Connect joins a namespace and returns an NSConn, the value used to
// send and receive on that namespace. Client.Conn returns the underlying
// Conn, and Client.NotifyClose, a channel, reports when the connection ends.
//
// # Handlers
//
// A ConnHandler is one of Events (event name to callback, for a single
// unnamed namespace), Namespaces (namespace to Events), WithTimeout (either
// of those plus timeouts and limits), or a Struct built with NewStruct (a Go
// struct whose methods become event handlers). JoinConnHandlers combines
// several into one, which helps when a client and a server share most of
// their handlers but each add a few of their own. The same ConnHandler
// value, or an equivalent one, is normally passed to both New and Dial.
//
// # Messages
//
// A Message carries a Namespace, an optional Room, an Event name and a
// Body. NSConn.Emit and Room.Emit send one without waiting for a reply.
// NSConn.Ask and Conn.Ask send one and block for a reply, a CloseError, or
// until ctx is done. Returning Reply(body) from an event handler answers an
// Ask; returning a plain error sends that error back instead. NSConn.Send
// and Room.Send send an event and report whether the write itself
// succeeded, rather than whether a handler should keep looping.
//
// # Backends
//
// Three backend packages adapt a specific websocket library to the Socket
// interface neffos needs: gorilla (gorilla/websocket), gobwas (gobwas/ws)
// and coder (coder/websocket). Each exports a DefaultUpgrader and a
// DefaultDialer, plus a constructor, Upgrader or Dialer, for a customised
// instance. The three are interchangeable on the wire: a client built on
// one backend talks to a server built on another, because every one of
// them speaks plain websocket text and binary frames.
//
// # Timeouts, heartbeat and limits
//
// WithTimeout.ReadTimeout and WriteTimeout bound a read and a write.
// WithTimeout.PingInterval, above zero, starts a heartbeat: the connection
// pings the remote side on that interval and closes if no pong arrives
// within it. WithTimeout.MaxMessageSize, above zero, caps the size of one
// incoming message; a bigger one closes the connection with
// CloseMessageTooBig, and Conn.Err then reports ErrMessageTooBig. The
// heartbeat and the size cap both need a Socket that implements
// SocketPinger or SocketReadLimiter; the three backend packages do.
// Struct.SetPingInterval and Struct.SetMaxMessageSize set the same two
// options for a Struct-based handler.
//
// # Closing
//
// Conn.Terminate closes one connection with a close code and a reason;
// Conn.Close is Terminate(CloseNormalClosure, ""). Conn.Err reports the
// close error once a connection is closed, or nil while it stays open.
// Server.Close closes every connection at once and returns. Server.Shutdown
// does the same, then waits for every connection's reader goroutine,
// including any event callback still running, to return, or until its ctx
// is done. Use Shutdown, not Close, when the process itself is exiting.
//
// # Scaling out
//
// A StackExchange shares broadcasts and Ask replies across more than one
// neffos server, through a message broker. The stackexchange/redis and
// stackexchange/nats subpackages each implement one, on redis publish and
// subscribe and on nats publish and subscribe. A StackExchange that also
// implements StackExchangeCloser gets its Close called as part of
// Server.Close, which releases its broker connections and goroutines.
// Within one server, NSConn.Broadcast and NSConn.BroadcastOthers send to
// every local connection directly, without going through a StackExchange.
//
// # Links
//
// Source code and further documentation:
//
//	https://github.com/kataras/neffos
//
// Wiki, including the v0.1.0 migration guide:
//
//	https://github.com/kataras/neffos/wiki
//
// Runnable examples:
//
//	https://github.com/kataras/neffos/tree/main/_examples
//
// The matching JavaScript and TypeScript client, for browsers and Node.js:
//
//	https://github.com/kataras/neffos.js
package neffos
