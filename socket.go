package neffos

import "time"

// Optional Socket extensions.
//
// A Socket implementation may also implement any of the interfaces in this
// file. Each one is optional: Conn checks for it with a type assertion and
// falls back to the old behaviour when the socket does not have it.
//
//   - SocketCloser: Conn.Close and Conn.Terminate send a close frame with a
//     status code and reason through it. Without it, Conn closes NetConn().
//   - SocketPinger: with WithTimeout.PingInterval > 0, Conn pings the remote
//     side on that interval and closes the connection when a ping fails.
//     Without it, PingInterval does nothing.
//   - SocketReadLimiter: with WithTimeout.MaxMessageSize > 0, Conn sets the
//     read limit once when the connection starts. Without it,
//     MaxMessageSize does nothing.

// SocketCloser is implemented by a Socket that can close the websocket
// connection with a status code and a reason.
type SocketCloser interface {
	// Close sends a close frame carrying code and reason, then closes the
	// underlying connection. timeout bounds the write of the close frame.
	//
	// Conn calls Close while other goroutines may be inside ReadData,
	// WriteText or WriteBinary, so the close frame write must be serialised
	// with the socket's other writes.
	Close(code int, reason string, timeout time.Duration) error
}

// SocketPinger is implemented by a Socket that can send websocket pings.
type SocketPinger interface {
	// Ping sends a ping frame. A timeout above zero also waits up to that
	// long for the pong and returns a timeout error when none arrives. A zero
	// timeout only sends the ping.
	//
	// Conn calls Ping from its heartbeat goroutine while other goroutines may
	// be inside ReadData, WriteText or WriteBinary, so the ping write must be
	// serialised with the socket's other writes. The pong arrives through the
	// goroutine inside ReadData.
	Ping(timeout time.Duration) error
}

// SocketReadLimiter is implemented by a Socket that can cap the size of
// incoming messages.
type SocketReadLimiter interface {
	// SetReadLimit sets the largest message, in bytes, the socket accepts.
	// A bigger message makes ReadData return ErrMessageTooBig after the
	// socket sends a close frame with CloseMessageTooBig. n <= 0 removes
	// the cap.
	SetReadLimit(n int64)
}
