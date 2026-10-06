// Package coder is the neffos backend for github.com/coder/websocket.
//
// Use Upgrader or DefaultUpgrader on the server (neffos.New) and Dialer or
// DefaultDialer on the client (neffos.Dial). Socket implements neffos.Socket
// and the optional neffos.SocketCloser, neffos.SocketPinger and
// neffos.SocketReadLimiter, so close codes, the heartbeat
// (WithTimeout.PingInterval) and WithTimeout.MaxMessageSize all work.
//
// Defaults that differ from coder's own, or that are easy to miss:
//
//   - No read limit. coder caps messages at 32 KB by default; the socket turns
//     that off, so a message of any size is read unless MaxMessageSize is set,
//     the same as the gorilla and gobwas backends.
//   - Same-origin requests only. Accept rejects a request whose Origin header
//     names another host with 403, as gorilla's default upgrader does. Set
//     AcceptOptions.OriginPatterns to allow other origins.
//   - On the client side, NetConn().RemoteAddr() and LocalAddr() return
//     coder's placeholder address, not the real one, because coder does not
//     expose the dialed connection.
//   - NetConn().Close() closes the connection at once with no close frame.
//     Use Socket.Close, or neffos.Conn.Close, to send one.
//   - A read or write timeout closes the connection. That is how coder
//     handles an expired context, and neffos closes the connection after a
//     timeout anyway.
package coder
