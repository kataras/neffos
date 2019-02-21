package fastws

import (
	"context"
	"net"
	"strings"

	"github.com/gobwas/ws"
)

// ErrBadHandshake is returned when the server response to opening handshake is
// invalid.
var ErrBadHandshake = ws.ErrHandshakeBadConnection

// Dial creates a new client connection.
//
// The context will be used in the request and in the Dialer.
//
// If the WebSocket handshake fails, `ErrHandshakeBadConnection` is returned.
//
// The "url" input parameter is the url to connect to the server, it should be
// the ws:// (or wss:// if secure) + the host + the endpoint of the
// open socket of the server, i.e ws://localhost:8080/my_websocket_endpoint.
//
// Custom dialers can be used by wrapping the iris websocket connection via `websocket.WrapConnection`.
func Dial(ctx context.Context, url string) (*Conn, error) {
	return dial(ctx, url)
}

func dial(ctx context.Context, url string) (*Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		url = "ws://" + url
	}

	// for some reason the underline Dialer does not hanle deadlines as expected.
	deadline, hasDeadline := ctx.Deadline()

	conn, _, hs, err := ws.DefaultDialer.Dial(ctx, url)
	if err != nil {
		return nil, err
	}

	if hasDeadline {
		conn.SetDeadline(deadline)
	}

	return WrapConn(conn, hs, ws.StateClientSide), nil
}

// WrapConn can be used to wrap the result of a custom ws.Dialer#Dial operation.
func WrapConn(conn net.Conn, hs ws.Handshake, state ws.State) *Conn {
	c := new(Conn)
	c.establish(conn, hs, state)
	return c
}
