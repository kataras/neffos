package coder

import (
	"context"

	"github.com/kataras/neffos"

	"github.com/coder/websocket"
)

// Options is the coder/websocket dial options type, so callers of Dialer do
// not need to import coder/websocket for it.
type Options = websocket.DialOptions

// DefaultDialer is a coder/websocket dialer with all options left at their
// zero values.
var DefaultDialer = Dialer(nil)

// Dialer is a `neffos.Dialer` type for the coder/websocket implementation.
// Use it on `neffos.Dial` to create a client connection. opts may be nil.
//
// The options go to websocket.Dial as they are. A set OnPongReceived still
// runs; the socket chains its own pong handling after it.
func Dialer(opts *websocket.DialOptions) neffos.Dialer {
	return func(ctx context.Context, url string) (neffos.Socket, error) {
		var o websocket.DialOptions
		if opts != nil {
			o = *opts
		}

		s := newSocket(nil)
		o.OnPongReceived = s.pongHandler(o.OnPongReceived)

		underline, _, err := websocket.Dial(ctx, url, &o)
		if err != nil {
			return nil, err
		}

		s.init(underline)
		return s, nil
	}
}
