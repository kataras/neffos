package coder

import (
	"net/http"

	"github.com/kataras/neffos"

	"github.com/coder/websocket"
)

// DefaultUpgrader is a coder/websocket upgrader with all options left at
// their zero values. It accepts same-origin requests only.
var DefaultUpgrader = Upgrader(websocket.AcceptOptions{})

// Upgrader is a `neffos.Upgrader` type for the coder/websocket
// implementation. Use it on `neffos.New` to construct the neffos server.
//
// The options go to websocket.Accept as they are, so OriginPatterns,
// Subprotocols, CompressionMode and InsecureSkipVerify work as coder
// documents them. A set OnPongReceived still runs; the socket chains its own
// pong handling after it.
func Upgrader(opts websocket.AcceptOptions) neffos.Upgrader {
	return func(w http.ResponseWriter, r *http.Request) (neffos.Socket, error) {
		s := newSocket(r)
		o := opts
		o.OnPongReceived = s.pongHandler(opts.OnPongReceived)

		underline, err := websocket.Accept(w, r, &o)
		if err != nil {
			return nil, err
		}

		s.init(underline)
		return s, nil
	}
}
