package gobwas

import (
	"context"

	"github.com/WolffunGame/wolfsocket"

	gobwas "github.com/gobwas/ws"
)

// DefaultDialer is a gobwas/ws dialer with all fields set to the default values.
var DefaultDialer = Dialer(gobwas.DefaultDialer)

// Dialer is a `neffos.Dialer` type for the gobwas/ws subprotocol implementation.
// Should be used on `Dial` to create a new client/client-side connection.
// To send headers to the server set the dialer's `Header` field to a `gobwas.HandshakeHeaderHTTP`.
func Dialer(dialer gobwas.Dialer) wolfsocket.Dialer {
	return func(ctx context.Context, url string) (wolfsocket.Socket, error) {
		underline, _, _, err := dialer.Dial(ctx, url)
		if err != nil {
			return nil, err
		}

		return newSocket(underline, nil, true), nil
	}
}
